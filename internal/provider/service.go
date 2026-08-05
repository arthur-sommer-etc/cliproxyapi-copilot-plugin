package provider

import (
	"sync"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

const providerID = "copilot"

type Service struct {
	host transport.Host
	now  func() time.Time

	configMu sync.RWMutex
	config   Config

	oauthMu      sync.Mutex
	oauthSession map[string]*deviceSession

	tokenMu       sync.Mutex
	tokenEntries  map[string]copilotTokenEntry
	tokenInflight map[string]*tokenFlight

	modelMu      sync.Mutex
	modelEntries map[string]modelCacheEntry
}

func New(host transport.Host) *Service {
	return &Service{
		host:          host,
		now:           time.Now,
		config:        DefaultConfig(),
		oauthSession:  make(map[string]*deviceSession),
		tokenEntries:  make(map[string]copilotTokenEntry),
		tokenInflight: make(map[string]*tokenFlight),
		modelEntries:  make(map[string]modelCacheEntry),
	}
}

func (s *Service) Configure(raw []byte) error {
	cfg, errParse := ParseConfig(raw)
	if errParse != nil {
		return errParse
	}
	s.configMu.Lock()
	s.config = cfg
	s.configMu.Unlock()
	return nil
}

func (s *Service) Config() Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}

func (s *Service) Shutdown() {
	s.oauthMu.Lock()
	clear(s.oauthSession)
	s.oauthMu.Unlock()
	s.tokenMu.Lock()
	clear(s.tokenEntries)
	s.tokenMu.Unlock()
	s.modelMu.Lock()
	clear(s.modelEntries)
	s.modelMu.Unlock()
}
