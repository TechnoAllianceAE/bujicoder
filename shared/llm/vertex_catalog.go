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
	modelsByID := make(map[string]ModelInfo)
	var successCount int
	var firstErr error
	for i, publisher := range defaultVertexPublishers {
		if i > 0 {
			// Light backpressure to avoid bursty request storms across publishers.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err := v.fetchPublisherModels(ctx, publisher, modelsByID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		successCount++
	}
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

		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return err
		}

		resp, err := v.client.Do(req)
		cancel()
		if err != nil {
			return err
		}
		// Skip unavailable publishers gracefully — not all publishers
		// are available in every region or project. 403/404 is expected
		// for publishers like x-ai, deepseek, qwen that aren't in Model Garden.
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
			resp.Body.Close()
			return fmt.Errorf("vertex list %s: unexpected status %d: %s", publisher, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var body vertexPublisherModelsResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			resp.Body.Close()
			return err
		}
		resp.Body.Close()
		for _, model := range body.PublisherModels {
			info, ok := vertexPublisherModelToInfo(model)
			if !ok {
				continue
			}
			out[info.ID] = info
		}
		if body.NextPageToken == "" {
			return nil
		}
		pageToken = body.NextPageToken
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
