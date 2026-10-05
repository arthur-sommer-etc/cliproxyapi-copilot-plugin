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

	configMu         sync.RWMutex
	config           Config
	configGeneration uint64

	oauthMu      sync.Mutex
	oauthSession map[string]*deviceSession

	tokenMu       sync.Mutex
	tokenEntries  map[string]copilotTokenEntry
	tokenInflight map[string]*tokenFlight

	modelMu      sync.Mutex
	modelEntries map[string]modelCacheEntry

	replayMu      sync.Mutex
	replayEntries map[string]reasoningReplayEntry
	replayBytes   int
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
		replayEntries: make(map[string]reasoningReplayEntry),
	}
}

func (s *Service) Configure(raw []byte) error {
	cfg, errParse := ParseConfig(raw)
	if errParse != nil {
		return errParse
	}
	s.configMu.Lock()
	s.config = cfg
	s.configGeneration++
	s.tokenMu.Lock()
	clear(s.tokenEntries)
	s.tokenMu.Unlock()
	s.configMu.Unlock()
	s.modelMu.Lock()
	clear(s.modelEntries)
	s.modelMu.Unlock()
	s.clearReasoningReplay()
	return nil
}

func (s *Service) Config() Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}

func (s *Service) configSnapshot() (Config, uint64) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config, s.configGeneration
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
	s.clearReasoningReplay()
}
