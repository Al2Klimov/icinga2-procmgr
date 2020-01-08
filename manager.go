package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type manager struct {
	reedis *redis.Client
	ourEnv struct {
		vars []string
		once sync.Once
	}
}

func (m *manager) readLoop() {
	me, errNR := uuid.NewRandom()
	if errNR != nil {
		fmt.Fprintf(os.Stderr, "%#v\n", errNR)
		return
	}

	{
		_, errGC := m.reedis.XGroupCreateMkStream("icinga2:process:spawn", "icinga2-procmgr", "0-0").Result()
		if errGC != nil && !strings.HasPrefix(errGC.Error(), "BUSYGROUP ") {
			fmt.Fprintf(os.Stderr, "%#v\n", errGC)
			return
		}
	}

	for {
		streams, errRG := m.reedis.XReadGroup(&redis.XReadGroupArgs{
			Group:    "icinga2-procmgr",
			Consumer: me.String(),
			Streams:  []string{"icinga2:process:spawn", ">"},
			Count:    100,
		}).Result()
		if errRG != nil {
			fmt.Fprintf(os.Stderr, "%#v\n", errRG)
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
		m.ackMsg(m.reedis, message)
		return
	}

	rawCommand, ok := message.Values["command"].(string)
	if !ok {
		m.sendFailure(message, rawId, "Bad command spec")
		return
	}

	var command []string
	if errJU := json.Unmarshal([]byte(rawCommand), &command); errJU != nil {
		m.sendFailure(message, rawId, "Bad command spec: "+errJU.Error())
		return
	}

	if len(command) < 1 {
		m.sendFailure(message, rawId, "Bad command spec")
		return
	}

	rawEnv, ok := message.Values["env"].(string)
	if !ok {
		m.sendFailure(message, rawId, "Bad env spec")
		return
	}

	var env []string
	if errJU := json.Unmarshal([]byte(rawEnv), &env); errJU != nil {
		m.sendFailure(message, rawId, "Bad env spec: "+errJU.Error())
		return
	}

	rawTimeout, ok := message.Values["timeout"].(string)
	if !ok {
		m.sendFailure(message, rawId, "Bad timeout spec")
		return
	}

	timeout, errPF := strconv.ParseFloat(rawTimeout, 64)
	if errPF != nil {
		m.sendFailure(message, rawId, "Bad timeout spec: "+errPF.Error())
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
	m.sendResponse(message, rawId, -1, 128, []byte(reason), now, now)
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

	if _, errEx := tx.Exec(); errEx != nil {
		fmt.Fprintf(os.Stderr, "%#v\n", errEx)
		return
	}
}

func (m *manager) ackMsg(client redis.Cmdable, message redis.XMessage) {
	if _, errXA := client.XAck("icinga2:process:spawn", "icinga2-procmgr", message.ID).Result(); errXA != nil {
		fmt.Fprintf(os.Stderr, "%#v\n", errXA)
		return
	}
}

func (m *manager) initEnv() {
	m.ourEnv.vars = os.Environ()
}
