package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	cfgpkg "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

const defaultUpstreamModelSyncInterval = 30 * time.Minute
const defaultUpstreamModelSyncTimeout = 20 * time.Second

type UpstreamModelSource struct {
	ID       string
	Name     string
	Provider string
	BaseURL  string
	APIKeys  []string
	Headers  map[string]string
	Timeout  time.Duration
}

type UpstreamSyncStatus struct {
	SourceID      string    `json:"source_id"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	ModelCount    int       `json:"model_count"`
}

type UpstreamModelAdapter interface {
	Name() string
	DiscoverModels(ctx context.Context, src UpstreamModelSource) ([]*ModelInfo, error)
}

type UpstreamModelSyncer struct {
	registry *ModelRegistry
	interval time.Duration

	mu        sync.RWMutex
	sources   []UpstreamModelSource
	knownIDs  map[string]struct{}
	status    map[string]*UpstreamSyncStatus
	adapters  map[string]UpstreamModelAdapter
	triggerCh chan struct{}
	startOnce sync.Once
}

func NewUpstreamModelSyncer(reg *ModelRegistry, interval time.Duration) *UpstreamModelSyncer {
	if interval <= 0 {
		interval = defaultUpstreamModelSyncInterval
	}
	return &UpstreamModelSyncer{
		registry: reg,
		interval: interval,
		knownIDs: make(map[string]struct{}),
		status:   make(map[string]*UpstreamSyncStatus),
		adapters: map[string]UpstreamModelAdapter{
			"openai-compat": &OpenAICompatAdapter{},
		},
		triggerCh: make(chan struct{}, 1),
	}
}

func (s *UpstreamModelSyncer) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		go s.loop(ctx)
	})
}

func (s *UpstreamModelSyncer) loop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.syncAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncAll(ctx)
		case <-s.triggerCh:
			s.syncAll(ctx)
		}
	}
}

func (s *UpstreamModelSyncer) UpdateSources(sources []UpstreamModelSource) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]UpstreamModelSource, 0, len(sources))
	nextIDs := make(map[string]struct{}, len(sources))
	for _, src := range sources {
		if strings.TrimSpace(src.ID) == "" {
			continue
		}
		next = append(next, src)
		nextIDs[src.ID] = struct{}{}
	}

	for id := range s.knownIDs {
		if _, ok := nextIDs[id]; ok {
			continue
		}
		s.registry.UnregisterClient(clientIDForSource(id))
		delete(s.status, id)
	}

	s.sources = next
	s.knownIDs = nextIDs
	select {
	case s.triggerCh <- struct{}{}:
	default:
	}
}

func (s *UpstreamModelSyncer) GetStatus() map[string]*UpstreamSyncStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*UpstreamSyncStatus, len(s.status))
	for k, v := range s.status {
		cp := *v
		out[k] = &cp
	}
	return out
}

func (s *UpstreamModelSyncer) syncAll(ctx context.Context) {
	s.mu.RLock()
	sources := append([]UpstreamModelSource(nil), s.sources...)
	s.mu.RUnlock()
	for _, src := range sources {
		s.syncOne(ctx, src)
	}
}

func (s *UpstreamModelSyncer) syncOne(ctx context.Context, src UpstreamModelSource) {
	st := &UpstreamSyncStatus{SourceID: src.ID, LastAttemptAt: time.Now()}
	defer func() {
		s.mu.Lock()
		s.status[src.ID] = st
		s.mu.Unlock()
	}()

	adapter, ok := s.adapters[src.Provider]
	if !ok {
		st.LastError = fmt.Sprintf("no adapter for provider %s", src.Provider)
		return
	}
	models, err := adapter.DiscoverModels(ctx, src)
	if err != nil {
		st.LastError = err.Error()
		log.Warnf("upstream model sync failed for %s: %v", src.ID, err)
		return
	}
	if len(models) == 0 {
		st.LastError = "no models discovered"
		log.Warnf("upstream model sync found no models for %s", src.ID)
		return
	}
	provider := src.Provider
	if provider == "openai-compat" {
		provider = "openai"
	}
	s.registry.RegisterClient(clientIDForSource(src.ID), provider, models)
	st.ModelCount = len(models)
	st.LastSuccessAt = time.Now()
	log.Infof("upstream model sync success for %s: %d models", src.ID, len(models))
}

func clientIDForSource(id string) string {
	return "upstream-sync:" + id
}

func BuildUpstreamSourcesFromConfig(cfg *cfgpkg.Config) []UpstreamModelSource {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return nil
	}
	out := make([]UpstreamModelSource, 0, len(cfg.OpenAICompatibility))
	for _, entry := range cfg.OpenAICompatibility {
		if !entry.AutoDiscoverModels {
			continue
		}
		baseURL := normalizeModelsBaseURL(entry.BaseURL)
		if baseURL == "" {
			continue
		}
		apiKeys := make([]string, 0, len(entry.APIKeyEntries))
		for _, keyEntry := range entry.APIKeyEntries {
			key := strings.TrimSpace(keyEntry.APIKey)
			if key == "" {
				continue
			}
			apiKeys = append(apiKeys, key)
		}
		out = append(out, UpstreamModelSource{
			ID:       strings.TrimSpace(entry.Name),
			Name:     strings.TrimSpace(entry.Name),
			Provider: "openai-compat",
			BaseURL:  baseURL,
			APIKeys:  apiKeys,
			Headers:  cloneHeaders(entry.Headers),
			Timeout:  defaultUpstreamModelSyncTimeout,
		})
	}
	return out
}

func normalizeModelsBaseURL(base string) string {
	base = strings.TrimSpace(base)
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = v
	}
	return out
}

type OpenAICompatAdapter struct{}

func (a *OpenAICompatAdapter) Name() string { return "openai-compat" }

type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (a *OpenAICompatAdapter) DiscoverModels(ctx context.Context, src UpstreamModelSource) ([]*ModelInfo, error) {
	url := strings.TrimRight(src.BaseURL, "/") + "/models"
	apiKeys := src.APIKeys
	if len(apiKeys) == 0 {
		apiKeys = []string{""}
	}
	client := &http.Client{Timeout: src.Timeout}
	merged := make([]*ModelInfo, 0)
	seen := make(map[string]struct{})
	var errs []string
	for _, apiKey := range apiKeys {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		for k, v := range src.Headers {
			if strings.EqualFold(k, "authorization") && apiKey != "" {
				continue
			}
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			errs = append(errs, fmt.Sprintf("status=%d", resp.StatusCode))
			continue
		}
		var out openAIModelsResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			errs = append(errs, err.Error())
			continue
		}
		resp.Body.Close()
		for _, item := range out.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			if info := LookupStaticModelInfo(id); info != nil {
				merged = append(merged, cloneModelInfo(info))
				continue
			}
			merged = append(merged, &ModelInfo{ID: id, Object: "model", Created: time.Now().Unix(), OwnedBy: src.Name, Type: "openai"})
		}
	}
	if len(merged) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("discover models failed for all keys: %s", strings.Join(errs, "; "))
	}
	return merged, nil
}
