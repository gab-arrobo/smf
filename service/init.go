// SPDX-FileCopyrightText: 2022-present Intel Corporation
// SPDX-FileCopyrightText: 2021 Open Networking Foundation <info@opennetworking.org>
// Copyright 2019 free5GC.org
//
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"net/http"
	_ "net/http/pprof" // Using package only for invoking initialization.
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	aperLogger "github.com/omec-project/aper/logger"
	grpcClient "github.com/omec-project/config5g/proto/client"
	protos "github.com/omec-project/config5g/proto/sdcoreConfig"
	nasLogger "github.com/omec-project/nas/logger"
	ngapLogger "github.com/omec-project/ngap/logger"
	openapiLogger "github.com/omec-project/openapi/logger"
	"github.com/omec-project/openapi/models"
	nrfCache "github.com/omec-project/openapi/nrfcache"
	"github.com/omec-project/smf/callback"
	"github.com/omec-project/smf/consumer"
	"github.com/omec-project/smf/context"
	"github.com/omec-project/smf/eventexposure"
	"github.com/omec-project/smf/factory"
	"github.com/omec-project/smf/logger"
	"github.com/omec-project/smf/metrics"
	"github.com/omec-project/smf/oam"
	"github.com/omec-project/smf/pdusession"
	"github.com/omec-project/smf/pfcp"
	"github.com/omec-project/smf/pfcp/message"
	"github.com/omec-project/smf/pfcp/udp"
	"github.com/omec-project/smf/pfcp/upf"
	"github.com/omec-project/util/http2_util"
	utilLogger "github.com/omec-project/util/logger"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type SMF struct{}

type (
	// Config information.
	Config struct {
		cfg       string
		uerouting string
	}
)

var refreshNrfRegistration bool

var config Config

var smfCLi = []cli.Flag{
	&cli.StringFlag{
		Name:     "cfg",
		Usage:    "smf config file",
		Required: true,
	},
	&cli.StringFlag{
		Name:     "uerouting",
		Usage:    "uerouting config file",
		Required: true,
	},
}

var (
	KeepAliveTimer      *time.Timer
	KeepAliveTimerMutex sync.Mutex
)

type OneInstance struct {
	m    sync.Mutex
	done uint32
}

var nrfRegInProgress OneInstance

func init() {
	nrfRegInProgress = OneInstance{}
}

func (*SMF) GetCliCmd() (flags []cli.Flag) {
	return smfCLi
}

func (smf *SMF) Initialize(c *cli.Command) error {
	config = Config{
		cfg:       c.String("cfg"),
		uerouting: c.String("uerouting"),
	}

	absPath, err := filepath.Abs(config.cfg)
	if err != nil {
		logger.CfgLog.Errorln(err)
		return err
	}

	if err = factory.InitConfigFactory(absPath); err != nil {
		return err
	}

	factory.SmfConfig.CfgLocation = absPath

	ueRoutingPath, err := filepath.Abs(config.uerouting)
	if err != nil {
		logger.CfgLog.Errorln(err)
		return err
	}

	if err := factory.InitRoutingConfigFactory(ueRoutingPath); err != nil {
		return err
	}

	smf.setLogLevel()

	if err := factory.CheckConfigVersion(); err != nil {
		return err
	}

	// Initiating a server for profiling
	if factory.SmfConfig.Configuration.DebugProfilePort != 0 {
		addr := fmt.Sprintf(":%d", factory.SmfConfig.Configuration.DebugProfilePort)
		go func() {
			err := http.ListenAndServe(addr, nil)
			if err != nil {
				logger.InitLog.Warnf("start profiling server failed: %+v", err)
			}
		}()
	}

	if os.Getenv("MANAGED_BY_CONFIG_POD") == "true" {
		logger.InitLog.Infoln("MANAGED_BY_CONFIG_POD is true")
		go manageGrpcClient(factory.SmfConfig.Configuration.WebuiUri)
	}
	return nil
}

// manageGrpcClient connects the config pod GRPC server and subscribes the config changes.
// Then it updates SMF configuration.
func manageGrpcClient(webuiUri string) {
	var configChannel chan *protos.NetworkSliceResponse
	var client grpcClient.ConfClient
	var stream protos.ConfigService_NetworkSliceSubscribeClient
	var err error
	count := 0
	for {
		if client != nil {
			if client.CheckGrpcConnectivity() != "READY" {
				time.Sleep(time.Second * 30)
				count++
				if count > 5 {
					err = client.GetConfigClientConn().Close()
					if err != nil {
						logger.InitLog.Infof("failing ConfigClient is not closed properly: %+v", err)
					}
					client = nil
					count = 0
				}
				logger.InitLog.Infoln("checking the connectivity readiness")
				continue
			}

			if stream == nil {
				stream, err = client.SubscribeToConfigServer()
				if err != nil {
					logger.InitLog.Infof("failing SubscribeToConfigServer: %+v", err)
					continue
				}
			}

			if configChannel == nil {
				configChannel = client.PublishOnConfigChange(true, stream)
				logger.InitLog.Infoln("PublishOnConfigChange is triggered")
				go factory.SmfConfig.UpdateConfig(configChannel)
				logger.InitLog.Infoln("SMF updateConfig is triggered")
			}

			time.Sleep(time.Second * 5) // Fixes (avoids) 100% CPU utilization
		} else {
			client, err = grpcClient.ConnectToConfigServer(webuiUri)
			stream = nil
			configChannel = nil
			logger.InitLog.Infoln("connecting to config server")
			if err != nil {
				logger.InitLog.Errorf("%+v", err)
			}
			continue
		}
	}
}

func (smf *SMF) setLogLevel() {
	if factory.SmfConfig.Logger == nil {
		logger.InitLog.Warnln("SMF config without log level setting")
		return
	}

	if factory.SmfConfig.Logger.SMF != nil {
		if factory.SmfConfig.Logger.SMF.DebugLevel != "" {
			if level, err := zapcore.ParseLevel(factory.SmfConfig.Logger.SMF.DebugLevel); err != nil {
				logger.InitLog.Warnf("SMF Log level [%s] is invalid, set to [info] level",
					factory.SmfConfig.Logger.SMF.DebugLevel)
				logger.SetLogLevel(zap.InfoLevel)
			} else {
				logger.InitLog.Infof("SMF Log level is set to [%s] level", level)
				logger.SetLogLevel(level)
			}
		} else {
			logger.InitLog.Infoln("SMF Log level is default set to [info] level")
			logger.SetLogLevel(zap.InfoLevel)
		}
	}

	if factory.SmfConfig.Logger.NAS != nil {
		if factory.SmfConfig.Logger.NAS.DebugLevel != "" {
			if level, err := zapcore.ParseLevel(factory.SmfConfig.Logger.NAS.DebugLevel); err != nil {
				nasLogger.NasLog.Warnf("NAS Log level [%s] is invalid, set to [info] level",
					factory.SmfConfig.Logger.NAS.DebugLevel)
				logger.SetLogLevel(zap.InfoLevel)
			} else {
				nasLogger.SetLogLevel(level)
			}
		} else {
			nasLogger.NasLog.Warnln("NAS Log level not set. Default set to [info] level")
			nasLogger.SetLogLevel(zap.InfoLevel)
		}
	}

	if factory.SmfConfig.Logger.NGAP != nil {
		if factory.SmfConfig.Logger.NGAP.DebugLevel != "" {
			if level, err := zapcore.ParseLevel(factory.SmfConfig.Logger.NGAP.DebugLevel); err != nil {
				ngapLogger.NgapLog.Warnf("NGAP Log level [%s] is invalid, set to [info] level",
					factory.SmfConfig.Logger.NGAP.DebugLevel)
				ngapLogger.SetLogLevel(zap.InfoLevel)
			} else {
				ngapLogger.SetLogLevel(level)
			}
		} else {
			ngapLogger.NgapLog.Warnln("NGAP Log level not set. Default set to [info] level")
			ngapLogger.SetLogLevel(zap.InfoLevel)
		}
	}

	if factory.SmfConfig.Logger.Aper != nil {
		if factory.SmfConfig.Logger.Aper.DebugLevel != "" {
			if level, err := zapcore.ParseLevel(factory.SmfConfig.Logger.Aper.DebugLevel); err != nil {
				aperLogger.AperLog.Warnf("Aper Log level [%s] is invalid, set to [info] level",
					factory.SmfConfig.Logger.Aper.DebugLevel)
				aperLogger.SetLogLevel(zap.InfoLevel)
			} else {
				aperLogger.SetLogLevel(level)
			}
		} else {
			aperLogger.AperLog.Warnln("Aper Log level not set. Default set to [info] level")
			aperLogger.SetLogLevel(zap.InfoLevel)
		}
	}

	if factory.SmfConfig.Logger.OpenApi != nil {
		if factory.SmfConfig.Logger.OpenApi.DebugLevel != "" {
			if level, err := zapcore.ParseLevel(factory.SmfConfig.Logger.OpenApi.DebugLevel); err != nil {
				openapiLogger.OpenapiLog.Warnf("OpenApi Log level [%s] is invalid, set to [info] level",
					factory.SmfConfig.Logger.OpenApi.DebugLevel)
				openapiLogger.SetLogLevel(zap.InfoLevel)
			} else {
				openapiLogger.SetLogLevel(level)
			}
		} else {
			openapiLogger.OpenapiLog.Warnln("OpenApi Log level not set. Default set to [info] level")
			openapiLogger.SetLogLevel(zap.InfoLevel)
		}
	}

	if factory.SmfConfig.Logger.Util != nil {
		if factory.SmfConfig.Logger.Util.DebugLevel != "" {
			if level, err := zapcore.ParseLevel(factory.SmfConfig.Logger.Util.DebugLevel); err != nil {
				utilLogger.UtilLog.Warnf("Util (drsm, fsm, etc.) Log level [%s] is invalid, set to [info] level",
					factory.SmfConfig.Logger.Util.DebugLevel)
				utilLogger.SetLogLevel(zap.InfoLevel)
			} else {
				utilLogger.SetLogLevel(level)
			}
		} else {
			utilLogger.UtilLog.Warnln("Util (drsm, fsm, etc.) Log level not set. Default set to [info] level")
			utilLogger.SetLogLevel(zap.InfoLevel)
		}
	}

	// Initialise Statistics
	go metrics.InitMetrics()
}

func (smf *SMF) FilterCli(c *cli.Command) (args []string) {
	for _, flag := range smf.GetCliCmd() {
		name := flag.Names()[0]
		value := fmt.Sprint(c.Generic(name))
		if value == "" {
			continue
		}

		args = append(args, "--"+name, value)
	}
	return args
}

func (smf *SMF) Start() {
	logger.InitLog.Infoln("SMF app initialising")

	// Initialise channel to stop SMF
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signalChannel
		smf.Terminate()
		os.Exit(0)
	}()

	// Init SMF Service
	smfCtxt := context.InitSmfContext(&factory.SmfConfig)

	if smfCtxt == nil {
		logger.InitLog.Fatalln("Failed to init smf context")
	}

	// allocate id for each upf
	context.AllocateUPFID()

	// Init UE Specific Config
	context.InitSMFUERouting(&factory.UERoutingConfig)

	// Wait for additional/updated config from config pod
	if os.Getenv("MANAGED_BY_CONFIG_POD") == "true" {
		logger.InitLog.Infof("configuration is managed by Config Pod")
		logger.InitLog.Infof("waiting for initial configuration from config pod")

		// Main thread should be blocked for config update from ROC
		// Future config update from ROC can be handled via background go-routine.
		if <-factory.ConfigPodTrigger {
			logger.InitLog.Infoln("minimum configuration from config pod available")
			context.ProcessConfigUpdate()
		}

		// Trigger background goroutine to handle further config updates
		go func() {
			logger.InitLog.Infoln("dynamic config update task initialised")
			for {
				if <-factory.ConfigPodTrigger {
					if context.ProcessConfigUpdate() {
						// Let NRF registration happen in background
						go smf.SendNrfRegistration()
					}
				}
			}
		}()
	} else {
		logger.InitLog.Infoln("configuration is managed by Helm")
	}

	// Send NRF Registration
	smf.SendNrfRegistration()

	if smfCtxt.EnableNrfCaching {
		logger.InitLog.Infof("enable NRF caching feature for %d seconds", smfCtxt.NrfCacheEvictionInterval)
		nrfCache.InitNrfCaching(smfCtxt.NrfCacheEvictionInterval*time.Second, consumer.SendNrfForNfInstance)
	}

	router := utilLogger.NewGinWithZap(logger.GinLog)
	oam.AddService(router)
	callback.AddService(router)
	for _, serviceName := range factory.SmfConfig.Configuration.ServiceNameList {
		switch models.ServiceName(serviceName) {
		case models.ServiceName_NSMF_PDUSESSION:
			pdusession.AddService(router)
		case models.ServiceName_NSMF_EVENT_EXPOSURE:
			eventexposure.AddService(router)
		}
	}

	if factory.SmfConfig.Configuration.EnableDbStore {
		logger.InitLog.Infoln("SetupSmfCollection")
		context.SetupSmfCollection()
		// Init DRSM for unique FSEID/FTEID/IP-Addr
		if err := smfCtxt.InitDrsm(); err != nil {
			logger.InitLog.Errorf("initialise drsm failed, %v ", err.Error())
		}
	} else {
		logger.InitLog.Infoln("DB is disabled, not initialising drsm")
	}

	// Init Kafka stream
	if err := metrics.InitialiseKafkaStream(factory.SmfConfig.Configuration); err != nil {
		logger.InitLog.Errorf("initialise kafka stream failed, %v ", err.Error())
	}

	udp.Run(pfcp.Dispatch)

	for _, upf := range context.SMF_Self().UserPlaneInformation.UPFs {
		if upf.NodeID.NodeIdType == context.NodeIdTypeFqdn {
			logger.AppLog.Infof("send PFCP Association Request to UPF[%s](%s)", upf.NodeID.NodeIdValue,
				upf.NodeID.ResolveNodeIdToIp().String())
		} else {
			logger.AppLog.Infof("send PFCP Association Request to UPF[%s]", upf.NodeID.ResolveNodeIdToIp().String())
		}
		err := message.SendPfcpAssociationSetupRequest(upf.NodeID, upf.Port)
		if err != nil {
			logger.AppLog.Errorf("send PFCP Association Request failed: %v", err)
		}
	}

	// Trigger PFCP Heartbeat towards all connected UPFs
	go upf.InitPfcpHeartbeatRequest(context.SMF_Self().UserPlaneInformation)

	// Trigger PFCP association towards not associated UPFs
	go upf.ProbeInactiveUpfs(context.SMF_Self().UserPlaneInformation)

	time.Sleep(1000 * time.Millisecond)

	HTTPAddr := fmt.Sprintf("%s:%d", context.SMF_Self().BindingIPv4, context.SMF_Self().SBIPort)
	sslLog := filepath.Dir(factory.SmfConfig.CfgLocation) + "/sslkey.log"
	server, err := http2_util.NewServer(HTTPAddr, sslLog, router)

	if server == nil {
		logger.InitLog.Errorln("initialize HTTP server failed:", err)
		return
	}

	if err != nil {
		logger.InitLog.Warnln("initialize HTTP server:", err)
	}

	serverScheme := factory.SmfConfig.Configuration.Sbi.Scheme
	switch serverScheme {
	case "http":
		err = server.ListenAndServe()
	case "https":
		err = server.ListenAndServeTLS(context.SMF_Self().PEM, context.SMF_Self().Key)
	default:
		logger.InitLog.Fatalf("HTTP server setup failed: invalid server scheme %+v", serverScheme)
		return
	}

	if err != nil {
		logger.InitLog.Fatalln("HTTP server setup failed:", err)
	}
}

func (smf *SMF) Terminate() {
	logger.InitLog.Infoln("terminating SMF")
	// deregister with NRF
	problemDetails, err := consumer.SendDeregisterNFInstance()
	if problemDetails != nil {
		logger.InitLog.Errorf("deregister NF instance Failed Problem[%+v]", problemDetails)
	} else if err != nil {
		logger.InitLog.Errorf("deregister NF instance Error[%+v]", err)
	} else {
		logger.InitLog.Infoln("deregister from NRF successfully")
	}
}

func (smf *SMF) Exec(c *cli.Command) error {
	return nil
}

func StartKeepAliveTimer(nfProfile *models.NfProfile) {
	KeepAliveTimerMutex.Lock()
	defer KeepAliveTimerMutex.Unlock()
	StopKeepAliveTimer()
	if nfProfile.HeartBeatTimer == 0 {
		nfProfile.HeartBeatTimer = 30
	}
	logger.InitLog.Infof("started KeepAlive Timer: %v sec", nfProfile.HeartBeatTimer)
	// AfterFunc starts timer and waits for KeepAliveTimer to elapse and then calls smf.UpdateNF function
	KeepAliveTimer = time.AfterFunc(time.Duration(nfProfile.HeartBeatTimer)*time.Second, UpdateNF)
}

func StopKeepAliveTimer() {
	if KeepAliveTimer != nil {
		logger.InitLog.Infoln("stopped KeepAlive Timer")
		KeepAliveTimer.Stop()
		KeepAliveTimer = nil
	}
}

// UpdateNF is the callback function, this is called when keepalivetimer elapsed
func UpdateNF() {
	KeepAliveTimerMutex.Lock()
	defer KeepAliveTimerMutex.Unlock()
	if KeepAliveTimer == nil {
		logger.InitLog.Warnln("keepAlive timer has been stopped")
		return
	}
	// setting default value 30 sec
	var heartBeatTimer int32 = 30
	pitem := models.PatchItem{
		Op:    "replace",
		Path:  "/nfStatus",
		Value: "REGISTERED",
	}
	var patchItem []models.PatchItem
	patchItem = append(patchItem, pitem)
	nfProfile, problemDetails, err := consumer.SendUpdateNFInstance(patchItem)
	if problemDetails != nil {
		logger.InitLog.Errorf("SMF update to NRF ProblemDetails[%v]", problemDetails)
		// 5xx response from NRF, 404 Not Found, 400 Bad Request
		if (problemDetails.Status/100) == 5 ||
			problemDetails.Status == 404 || problemDetails.Status == 400 {
			// register with NRF full profile
			nfProfile, err = consumer.SendNFRegistration()
			if err != nil {
				logger.InitLog.Errorf("error [%v] when sending NF registration", err)
			}
		}
	} else if err != nil {
		logger.InitLog.Errorf("SMF update to NRF Error[%s]", err.Error())
		nfProfile, err = consumer.SendNFRegistration()
		if err != nil {
			logger.InitLog.Errorf("error [%v] when sending NF registration", err)
		}
	}

	if nfProfile.HeartBeatTimer != 0 {
		// use hearbeattimer value with received timer value from NRF
		heartBeatTimer = nfProfile.HeartBeatTimer
	}
	logger.InitLog.Debugf("restarted KeepAlive Timer: %v sec", heartBeatTimer)
	// restart timer with received HeartBeatTimer value
	KeepAliveTimer = time.AfterFunc(time.Duration(heartBeatTimer)*time.Second, UpdateNF)
}

func (smf *SMF) SendNrfRegistration() {
	// If NRF registration is ongoing then don't start another in parallel
	// Just mark it so that once ongoing finishes then resend another
	if nrfRegInProgress.intanceRun(consumer.ReSendNFRegistration) {
		logger.InitLog.Infoln("NRF Registration already in progress...")
		refreshNrfRegistration = true
		return
	}

	// Once the first goroutine which was sending NRF registration returns,
	// Check if another fresh NRF registration is required
	if refreshNrfRegistration {
		refreshNrfRegistration = false
		if prof, err := consumer.SendNFRegistration(); err != nil {
			logger.InitLog.Infof("NRF Registration failure, %v", err.Error())
		} else {
			StartKeepAliveTimer(prof)
			logger.CfgLog.Infoln("sent Register NF Instance with updated profile")
		}
	}
}

// Run only single instance of func f at a time
func (o *OneInstance) intanceRun(f func() *models.NfProfile) bool {
	// Instance already running ?
	if atomic.LoadUint32(&o.done) == 1 {
		return true
	}

	// Slow-path.
	o.m.Lock()
	defer o.m.Unlock()
	if o.done == 0 {
		atomic.StoreUint32(&o.done, 1)
		defer atomic.StoreUint32(&o.done, 0)
		nfProfile := f()
		StartKeepAliveTimer(nfProfile)
	}
	return false
}
