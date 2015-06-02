package pixy

import (
	"fmt"
	"sync"

	"github.com/mailgun/kafka-pixy/Godeps/_workspace/src/github.com/mailgun/log"
)

type ServiceCfg struct {
	UnixAddr    string
	TCPAddr     string
	BrokerAddrs []string
}

type Service struct {
	kafkaClient *KafkaProxyImpl
	unixServer  *HTTPAPIServer
	tcpServer   *HTTPAPIServer
	quitCh      chan struct{}
	wg          sync.WaitGroup
}

func SpawnService(cfg *ServiceCfg) (*Service, error) {
	kafkaClientCfg := NewKafkaProxyCfg()
	kafkaClientCfg.BrokerAddrs = cfg.BrokerAddrs
	kafkaProxy, err := NewKafkaProxy(kafkaClientCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to spawn Kafka client, cause=(%v)", err)
	}

	unixServer, err := NewHTTPAPIServer(NetworkUnix, cfg.UnixAddr, kafkaProxy)
	if err != nil {
		kafkaProxy.Dispose()
		return nil, fmt.Errorf("failed to start Unix socket based HTTP API, cause=(%v)", err)
	}

	var tcpServer *HTTPAPIServer
	if cfg.TCPAddr != "" {
		if tcpServer, err = NewHTTPAPIServer(NetworkTCP, cfg.TCPAddr, kafkaProxy); err != nil {
			kafkaProxy.Dispose()
			return nil, fmt.Errorf("failed to start TCP socket based HTTP API, cause=(%v)", err)
		}
	}

	s := &Service{
		kafkaClient: kafkaProxy,
		unixServer:  unixServer,
		tcpServer:   tcpServer,
		quitCh:      make(chan struct{}),
	}

	goGo("Service Supervisor", &s.wg, s.supervisor)
	return s, nil
}

func (s *Service) Stop() {
	close(s.quitCh)
}

func (s *Service) Wait4Stop() {
	s.wg.Wait()
}

// supervisor takes care of the service graceful shutdown.
func (s *Service) supervisor() {
	var tcpServerErrorCh <-chan error

	s.kafkaClient.Start()
	s.unixServer.Start()
	if s.tcpServer != nil {
		s.tcpServer.Start()
		tcpServerErrorCh = s.tcpServer.ErrorCh()
	}
	// Block to wait for quit signal or an API server crash.
	select {
	case <-s.quitCh:
	case err, ok := <-s.unixServer.ErrorCh():
		if ok {
			log.Errorf("Unix socket based HTTP API crashed, cause=(%v)", err)
		}
	case err, ok := <-tcpServerErrorCh:
		if ok {
			log.Errorf("TCP socket based HTTP API crashed, cause=(%v)", err)
		}
	}
	// Initiate stop of all API servers.
	s.unixServer.Stop()
	if s.tcpServer != nil {
		s.tcpServer.Stop()
	}
	// Wait until all API servers are stopped.
	<-s.unixServer.ErrorCh()
	if s.tcpServer != nil {
		<-s.tcpServer.ErrorCh()
	}
	// Only when all API servers are stopped it is safe to stop the Kafka client.
	s.kafkaClient.Stop()
	s.kafkaClient.Wait4Stop()
}
