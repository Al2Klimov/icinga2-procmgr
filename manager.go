package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type manager struct {
	id     uuid.UUID
	reedis *redis.Client
	ourEnv struct {
		vars []string
		once sync.Once
	}
}

func (m *manager) readLoop() {
	{
		var errNR error
		m.id, errNR = uuid.NewRandom()

		if errNR != nil {
			log.WithFields(log.Fields{"error": errNR.Error()}).Fatal("couldn't generate new UUID")
			return
		}
	}

	{
		_, errGC := m.reedis.XGroupCreateMkStream("icinga2:process:spawn", "icinga2-procmgr", "0-0").Result()
		if errGC != nil && !strings.HasPrefix(errGC.Error(), "BUSYGROUP ") {
			log.WithFields(log.Fields{
				"stream": "icinga2:process:spawn",
				"group":  "icinga2-procmgr",
				"error":  errGC.Error(),
			}).Fatal("couldn't create Redis stream consumer group")
			return
		}
	}

	log.WithFields(log.Fields{"me": m.id}).Info("handling process spawn requests")

	for {
		streams, errRG := m.reedis.XReadGroup(&redis.XReadGroupArgs{
			Group:    "icinga2-procmgr",
			Consumer: m.id.String(),
			Streams:  []string{"icinga2:process:spawn", ">"},
			Count:    100,
		}).Result()

		if errRG != nil {
			log.WithFields(log.Fields{
				"stream": "icinga2:process:spawn",
				"group":  "icinga2-procmgr",
				"error":  errRG.Error(),
			}).Fatal("couldn't read from Redis stream")
			return
		}

		for _, stream := range streams {
			for _, message := range stream.Messages {
				go m.handleRequest(message)
			}
		}
	}
}

func (m *manager) handleRequest(message redis.XMessage) {
	rawId, ok := message.Values["id"].(string)
	if !ok {
		log.WithFields(log.Fields{
			"redis_id": message.ID,
		}).Warn("throwing away process spawn request w/o actual request ID")

		m.ackMsg(m.reedis, message)
		return
	}

	log.WithFields(log.Fields{"request": rawId}).Trace("got process spawn request")

	rawCommand, ok := message.Values["command"].(string)
	if !ok {
		m.sendFailure(message, rawId, "bad command spec")
		return
	}

	var command []string
	if errJU := json.Unmarshal([]byte(rawCommand), &command); errJU != nil {
		m.sendFailure(message, rawId, "bad command spec: "+errJU.Error())
		return
	}

	if len(command) < 1 {
		m.sendFailure(message, rawId, "bad command spec")
		return
	}

	rawEnv, ok := message.Values["env"].(string)
	if !ok {
		m.sendFailure(message, rawId, "bad env spec")
		return
	}

	var env []string
	if errJU := json.Unmarshal([]byte(rawEnv), &env); errJU != nil {
		m.sendFailure(message, rawId, "bad env spec: "+errJU.Error())
		return
	}

	rawTimeout, ok := message.Values["timeout"].(string)
	if !ok {
		m.sendFailure(message, rawId, "bad timeout spec")
		return
	}

	timeout, errPF := strconv.ParseFloat(rawTimeout, 64)
	if errPF != nil {
		m.sendFailure(message, rawId, "bad timeout spec: "+errPF.Error())
		return
	}

	m.ourEnv.once.Do(m.initEnv)

	cmd := exec.Command(command[0], command[1:]...)
	var out bytes.Buffer
	sharedOut := sharedWriter{writer: &out}

	cmd.Env = append(m.ourEnv.vars, env...)
	cmd.Stdout = &sharedOut
	cmd.Stderr = &sharedOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	critical.RLock()
	defer critical.RUnlock()

	log.WithFields(log.Fields{"command": command, "request": rawId}).Debug("running command")

	if errSt := cmd.Start(); errSt == nil {
		start := time.Now()

		timer := time.NewTimer(time.Duration(timeout * float64(time.Second)))
		defer timer.Stop()

		waitErr := make(chan error, 1)
		go waitForCmd(cmd, waitErr)

		var errWt error
		var end time.Time
		timerCh := timer.C

	Wait:
		for {
			select {
			case errWt = <-waitErr:
				end = time.Now()
				break Wait
			case <-timerCh:
				log.WithFields(log.Fields{"request": rawId}).Warn("timeout exceeded")

				sharedOut.Write([]byte("<Timeout exceeded.>"))
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				timerCh = nil
				break
			case <-shuttingDown:
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-waitErr
				return
			}
		}

		log.WithFields(log.Fields{"request": rawId}).Debug("process finished")

		if errWt == nil {
			m.sendResponse(message, rawId, cmd.Process.Pid, 0, out.Bytes(), start, end)
		} else if ee, ok := errWt.(*exec.ExitError); ok {
			var exitCode int
			status := ee.ProcessState.Sys().(syscall.WaitStatus)

			if status.Exited() {
				exitCode = status.ExitStatus()
			} else if status.Signaled() {
				exitCode = 128 + int(status.Signal())
				fmt.Fprintf(&sharedOut, "<Terminated by signal %s.>", status.Signal())
			} else if status.Stopped() {
				exitCode = 128 + int(status.StopSignal())
				fmt.Fprintf(&sharedOut, "<Terminated by signal %s.>", status.StopSignal())
			} else if status.Continued() {
				exitCode = 128 + int(syscall.SIGCONT)
				fmt.Fprintf(&sharedOut, "<Terminated by signal %s.>", syscall.SIGCONT)
			} else {
				exitCode = 128
				fmt.Fprintf(&sharedOut, "<%s>", ee.Error())
			}

			m.sendResponse(message, rawId, cmd.Process.Pid, exitCode, out.Bytes(), start, end)
		} else {
			fmt.Fprintf(&sharedOut, "<%s>", errWt.Error())
			m.sendResponse(message, rawId, -1, 128, out.Bytes(), start, end)
		}
	} else {
		m.sendFailure(message, rawId, errSt.Error())
	}
}

func (m *manager) sendFailure(message redis.XMessage, rawId string, reason string) {
	now := time.Now()

	log.WithFields(log.Fields{"request": rawId, "reason": reason}).Warn("couldn't handle process spawn request")

	m.sendResponse(
		message, rawId, -1, 128,
		[]byte(fmt.Sprintf("[Icinga 2 process manager %s] %s", m.id.String(), reason)),
		now, now,
	)
}

func (m *manager) sendResponse(message redis.XMessage, rawId string, pid, exitCode int, output []byte, execStart, execEnd time.Time) {
	tx := m.reedis.TxPipeline()

	tx.XAdd(&redis.XAddArgs{
		Stream: "icinga2:process:exit",
		ID:     "*",
		Values: map[string]interface{}{
			"id":         rawId,
			"pid":        strconv.FormatInt(int64(pid), 10),
			"code":       strconv.FormatInt(int64(exitCode), 10),
			"output":     string(output),
			"exec_start": strconv.FormatFloat(time2Float(execStart), 'f', -1, 64),
			"exec_end":   strconv.FormatFloat(time2Float(execEnd), 'f', -1, 64),
		},
	})

	m.ackMsg(tx, message)

	if _, errEx := tx.Exec(); errEx == nil {
		log.WithFields(log.Fields{"request": rawId}).Trace("responded to process spawn request")
	} else {
		log.WithFields(log.Fields{
			"request": rawId, "error": errEx.Error(),
		}).Error("couldn't respond to process spawn request")
	}
}

func (m *manager) ackMsg(client redis.Cmdable, message redis.XMessage) {
	if _, errXA := client.XAck("icinga2:process:spawn", "icinga2-procmgr", message.ID).Result(); errXA != nil {
		log.WithFields(log.Fields{
			"redis_id": message.ID, "error": errXA.Error(),
		}).Error("couldn't XACK process spawn request")
	}
}

func (m *manager) initEnv() {
	m.ourEnv.vars = os.Environ()
}
