package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type hubClassificationCapabilities struct {
	MaxCatalogFormat       int    `json:"maxCatalogFormat"`
	MinWriterFormat        int    `json:"minWriterFormat"`
	LogicalFileIdentity    bool   `json:"logicalFileIdentity"`
	WriterEpoch            int64  `json:"writerEpoch"`
	WriterRetired          bool   `json:"writerRetired"`
	HubWriterGate          bool   `json:"hubWriterGate"`
	LibraryMinWriterFormat int    `json:"libraryMinWriterFormat"`
	ClassificationMode     string `json:"classificationMode"`
}

func (c *cloudClient) hubClassificationCapabilities(ctx context.Context) (hubClassificationCapabilities, error) {
	return c.runtime.hubClassificationCapabilitiesFor(ctx, c.libraryID(), c.runtime.id)
}

func (c *cloudRuntime) hubClassificationCapabilitiesFor(ctx context.Context, libraryID, authorID string) (hubClassificationCapabilities, error) {
	var caps hubClassificationCapabilities
	if !validCloudHash(libraryID) || authorID == "" || len(authorID) > 128 {
		return caps, errors.New("invalid Hub capability identity")
	}
	wsURL, token, err := c.credentials()
	if err != nil {
		return caps, err
	}
	if wsURL == "" || len(token) < 32 {
		return caps, errors.New("Hub unavailable")
	}
	base, err := hubHTTPURL(wsURL)
	if err != nil {
		return caps, err
	}
	path := "/cloud/capabilities?libraryId=" + url.QueryEscape(libraryID) + "&authorId=" + url.QueryEscape(authorID)
	response, err := c.request(ctx, http.MethodGet, base, path, token, nil)
	if err != nil {
		return caps, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return caps, fmt.Errorf("Hub capabilities: HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&caps); err != nil {
		return caps, err
	}
	if caps.MaxCatalogFormat != 5 || caps.MinWriterFormat < 3 || caps.MinWriterFormat > 5 || caps.LibraryMinWriterFormat < 3 || caps.LibraryMinWriterFormat > 5 || !caps.HubWriterGate {
		return hubClassificationCapabilities{}, errors.New("Hub returned invalid catalog gates")
	}
	if caps.ClassificationMode != "uninitialized" && caps.ClassificationMode != "active" && caps.ClassificationMode != "unavailable" {
		return hubClassificationCapabilities{}, errors.New("Hub returned invalid classification mode")
	}
	if caps.ClassificationMode == "active" && caps.LibraryMinWriterFormat != 5 {
		return hubClassificationCapabilities{}, errors.New("Hub returned incomplete classification gate")
	}
	return caps, nil
}
