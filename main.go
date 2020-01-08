package main

import (
	"errors"
	"flag"
	"github.com/go-ini/ini"
	"github.com/go-redis/redis"
	log "github.com/sirupsen/logrus"
	"icinga2-procmgr/config"
	"net"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"
)

func main() {
	log.SetLevel(log.ErrorLevel)
	log.SetOutput(os.Stdout)

	cfgFile := flag.String("config", "", "config file")

	flag.Parse()

	if *cfgFile == "" {
		log.Fatal("config file missing")
		return
	}

	cfg, errCfg := loadConfig(*cfgFile)
	if errCfg != nil {
		log.Fatal(errCfg)
		return
	}

	errLog := setupLogging(cfg)
	if errLog != nil {
		log.Fatal(errLog)
		return
	}

	chSignal := make(chan os.Signal, 1)
	signal.Notify(chSignal, syscall.SIGINT, syscall.SIGTERM)

	{
		reedis := cfg["redis"]
		opts := &redis.Options{
			Network:      "tcp",
			Addr:         reedis["address"].(string),
			ReadTimeout:  time.Minute,
			WriteTimeout: time.Minute,
		}

		if path.IsAbs(opts.Addr) {
			opts.Network = "unix"
		}

		if password, ok := reedis["password"].(string); ok {
			opts.Password = password
		}

		if database, ok := reedis["database"].(uint64); ok {
			opts.DB = int(database)
		}

		go (&manager{reedis: redis.NewClient(opts)}).readLoop()
	}

	log.WithFields(log.Fields{"signal": <-chSignal}).Info("terminating due to signal")

	close(shuttingDown)
	critical.Lock()
}

// loadConfig reads, parses and validates the config from the given path.
func loadConfig(path string) (map[string]map[string]interface{}, error) {
	cfg, errCfg := ini.Load(path)
	if errCfg != nil {
		return nil, errCfg
	}

	rawCfg := map[string]map[string]string{}
	for _, section := range cfg.Sections() {
		rawCfg[section.Name()] = section.KeysHash()
	}

	delete(rawCfg, "DEFAULT")

	return (&config.Validator{
		"log": {
			"type": {
				PreCondition: config.NoPreCondition,
				Required:     config.Optional,
				Default:      "stdout",
				TypeParser:   config.TypeString,
				Validator:    config.OneOf([]string{"file", "stdout"}),
			},
			"path": {
				PreCondition: func(states map[string]map[string]*config.ParameterValidationState) bool {
					logType := states["log"]["type"].Value
					return logType != nil && logType.(string) == "file"
				},
				Required:   config.Required,
				TypeParser: config.TypeString,
				Validator:  config.NoValidator,
			},
			"level": {
				PreCondition: config.NoPreCondition,
				Required:     config.Optional,
				Default:      log.InfoLevel,
				TypeParser: func(s string) (interface{}, error) {
					switch s {
					case "fatal":
						return log.FatalLevel, nil
					case "error":
						return log.ErrorLevel, nil
					case "warning":
						return log.WarnLevel, nil
					case "info":
						return log.InfoLevel, nil
					case "debug":
						return log.DebugLevel, nil
					case "trace":
						return log.TraceLevel, nil
					default:
						return nil, errors.New("bad log level: " + s)
					}
				},
				Validator: config.NoValidator,
			},
			"format": {
				PreCondition: config.NoPreCondition,
				Required:     config.Optional,
				TypeParser:   config.TypeString,
				Default:      "text",
				Validator:    config.OneOf([]string{"text", "json"}),
			},
		},
		"redis": {
			"address": {
				PreCondition: config.NoPreCondition,
				Required:     config.Required,
				TypeParser:   config.TypeString,
				Validator:    validateRedisAddress,
			},
			"password": {
				PreCondition: config.NoPreCondition,
				Required:     config.Optional,
				TypeParser:   config.TypeString,
				Validator:    config.NoValidator,
			},
			"database": {
				PreCondition: config.NoPreCondition,
				Required:     config.Optional,
				TypeParser:   config.TypeUInt64,
				Validator:    config.NoValidator,
			},
		},
	}).Validate(rawCfg)
}

// setupLogging opens the configured log destination for logging.
func setupLogging(cfg map[string]map[string]interface{}) error {
	log.SetLevel(cfg["log"]["level"].(log.Level))

	if cfg["log"]["type"].(string) == "file" {
		logFile, err := os.OpenFile(cfg["log"]["path"].(string), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0640)
		if err != nil {
			return err
		}

		log.SetOutput(logFile)
	}

	if cfg["log"]["format"].(string) == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	}

	return nil
}

// validateRedisAddress validates a Redis server address.
func validateRedisAddress(addr interface{}) error {
	s := addr.(string)
	if path.IsAbs(s) {
		return nil
	}

	_, port, errSA := net.SplitHostPort(s)
	if errSA != nil {
		return errSA
	}

	_, errLP := net.LookupPort("tcp", port)
	return errLP
}
