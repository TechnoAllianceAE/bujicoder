package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

var defaultVertexPublishers = []string{
	"google",
	"anthropic",
	"meta",
	"mistralai",
	"openai",
	"x-ai",
	"deepseek",
	"qwen",
	"minimax",
	"moonshotai",
	"z-ai",
	"ai21",
}

type vertexPublisherModelsResponse struct {
	PublisherModels []vertexPublisherModel `json:"publisherModels"`
	NextPageToken   string                 `json:"nextPageToken"`
}

type vertexPublisherModel struct {
	Name                   string `json:"name"`
	VersionID              string `json:"versionId"`
	PublisherModelTemplate string `json:"publisherModelTemplate"`
}

func (v *VertexProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	v.catalogMu.RLock()
	if len(v.catalogCache) > 0 && time.Since(v.catalogLastRefreshed) < 15*time.Minute {
		models := append([]ModelInfo(nil), v.catalogCache...)
		v.catalogMu.RUnlock()
		return models, nil
	}
	v.catalogMu.RUnlock()
	return v.RefreshModelCatalog(ctx)
}

func (v *VertexProvider) RefreshModelCatalog(ctx context.Context) ([]ModelInfo, error) {
	log.Info().Str("host", v.vertexCatalogHost()).Str("region", v.region).Msg("vertex catalog: starting refresh")

	// Use a dedicated context with generous timeout instead of the caller's
	// context, which may be a short-lived HTTP request context. The Google
	// publisher alone can return 100+ models across multiple pages.
	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer refreshCancel()

	modelsByID := make(map[string]ModelInfo)
	var successCount int
	var firstErr error
	for i, publisher := range defaultVertexPublishers {
		if i > 0 {
			select {
			case <-refreshCtx.Done():
				return nil, refreshCtx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		before := len(modelsByID)
		if err := v.fetchPublisherModels(refreshCtx, publisher, modelsByID); err != nil {
			log.Warn().Err(err).Str("publisher", publisher).Msg("vertex catalog: publisher fetch failed")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		added := len(modelsByID) - before
		if added > 0 {
			log.Debug().Str("publisher", publisher).Int("models", added).Msg("vertex catalog: fetched publisher")
		}
		successCount++
	}

	log.Info().Int("publishers_ok", successCount).Int("total_models", len(modelsByID)).Msg("vertex catalog: refresh complete")

	if successCount == 0 && firstErr != nil {
		return nil, firstErr
	}

	models := make([]ModelInfo, 0, len(modelsByID))
	for _, model := range modelsByID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	v.catalogMu.Lock()
	v.catalogCache = append([]ModelInfo(nil), models...)
	v.catalogLastRefreshed = time.Now().UTC()
	v.catalogMu.Unlock()
	return models, nil
}

func (v *VertexProvider) CatalogLastRefreshed() time.Time {
	v.catalogMu.RLock()
	defer v.catalogMu.RUnlock()
	return v.catalogLastRefreshed
}

// vertexCatalogHost returns the Vertex AI platform host for model catalog requests.
// Uses the regional endpoint for specific regions, or the global endpoint otherwise.
func (v *VertexProvider) vertexCatalogHost() string {
	if v.region == "" || v.region == "global" {
		return "aiplatform.googleapis.com"
	}
	return v.region + "-aiplatform.googleapis.com"
}

func (v *VertexProvider) fetchPublisherModels(ctx context.Context, publisher string, out map[string]ModelInfo) error {
	pageToken := ""
	for {
		url := fmt.Sprintf(
			"https://%s/v1beta1/publishers/%s/models?pageSize=200&listAllVersions=true",
			v.vertexCatalogHost(),
			publisher,
		)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return err
		}

		resp, err := v.client.Do(req)
		cancel()
		if err != nil {
			// Log the URL for debugging (don't leak credentials — URL is public endpoint)
			log.Warn().Err(err).Str("publisher", publisher).Str("url", url).Msg("vertex catalog: HTTP request failed")
			return err
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			log.Debug().Str("publisher", publisher).Int("status", resp.StatusCode).Msg("vertex catalog: publisher not available, skipping")
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
			resp.Body.Close()
			return fmt.Errorf("vertex list %s: unexpected status %d: %s", publisher, resp.StatusCode, strings.TrimSpace(string(errBody)))
		}

		var respBody vertexPublisherModelsResponse
		if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
			resp.Body.Close()
			log.Warn().Err(err).Str("publisher", publisher).Msg("vertex catalog: failed to decode response")
			return err
		}
		resp.Body.Close()

		log.Debug().Str("publisher", publisher).Int("models_in_page", len(respBody.PublisherModels)).Str("nextPage", respBody.NextPageToken).Msg("vertex catalog: page received")

		for _, model := range respBody.PublisherModels {
			info, ok := vertexPublisherModelToInfo(model)
			if !ok {
				continue
			}
			out[info.ID] = info
		}
		if respBody.NextPageToken == "" {
			return nil
		}
		pageToken = respBody.NextPageToken
	}
}

func vertexPublisherModelToInfo(model vertexPublisherModel) (ModelInfo, bool) {
	resource := model.Name
	parts := strings.Split(resource, "/")
	if len(parts) < 4 {
		return ModelInfo{}, false
	}
	publisher := parts[len(parts)-3]
	modelID := parts[len(parts)-1]
	if modelID == "" {
		return ModelInfo{}, false
	}

	id := "vertex/" + publisher + "/" + modelID
	if publisher == "google" {
		id = "vertex/" + modelID
	}

	displayName := strings.ReplaceAll(modelID, "-", " ")
	displayName = strings.ReplaceAll(displayName, "_", " ")
	return ModelInfo{
		ID:     id,
		Name:   displayName,
		Source: "vertex",
	}, true
}
