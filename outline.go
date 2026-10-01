package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Outline is a minimal client for the parts of the Outline API we need.
type Outline struct {
	base  string
	token string
	http  *http.Client
}

// APIError is returned for non-2xx responses.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("outline API %d %s: %s", e.Status, e.Code, e.Message)
}

func IsForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusUnauthorized)
}

func NewOutline(base, token string) *Outline {
	return &Outline{
		base:  base,
		token: token,
		// Generous timeout: downloads of large collections can take a while.
		http: &http.Client{Timeout: 10 * time.Minute},
	}
}

// call POSTs a JSON body to /api/<method> and decodes {"data": ...} into out.
// Transient failures (network errors, 429, 5xx) are retried with backoff.
func (o *Outline) call(ctx context.Context, method string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
		lastErr = o.callOnce(ctx, method, payload, out)
		if lastErr == nil || !retryable(lastErr) || ctx.Err() != nil {
			return lastErr
		}
	}
	return lastErr
}

func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusTooManyRequests || ae.Status >= 500
	}
	return true // network-level error
}

func (o *Outline) callOnce(ctx context.Context, method string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/api/"+method, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := o.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		ae := &APIError{Status: res.StatusCode, Message: http.StatusText(res.StatusCode)}
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &body) == nil {
			ae.Code, ae.Message = body.Error, body.Message
		}
		return ae
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode response: %w", method, err)
	}
	return nil
}

type Collection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type pagination struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

const pageSize = 100

// Collections returns every collection the token can see (paginated).
func (o *Outline) Collections(ctx context.Context) ([]Collection, error) {
	var all []Collection
	for offset := 0; ; offset += pageSize {
		var res struct {
			Data []Collection `json:"data"`
		}
		if err := o.call(ctx, "collections.list", pagination{offset, pageSize}, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Data...)
		if len(res.Data) < pageSize {
			return all, nil
		}
	}
}

type FileOperation struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Format       string `json:"format"`
	State        string `json:"state"`
	Error        string `json:"error"`
	CollectionID string `json:"collectionId"`
	User         struct {
		ID string `json:"id"`
	} `json:"user"`
}

const exportFormat = "outline-markdown"

// StartExport asks Outline to build a markdown export of a collection and
// returns the id of the resulting file operation.
func (o *Outline) StartExport(ctx context.Context, collectionID string) (string, error) {
	var res struct {
		Data struct {
			FileOperation FileOperation `json:"fileOperation"`
		} `json:"data"`
	}
	err := o.call(ctx, "collections.export", map[string]string{"id": collectionID, "format": exportFormat}, &res)
	if err != nil {
		return "", err
	}
	if res.Data.FileOperation.ID == "" {
		return "", errors.New("collections.export returned no file operation")
	}
	return res.Data.FileOperation.ID, nil
}

// WaitForExport polls until the file operation is complete.
func (o *Outline) WaitForExport(ctx context.Context, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var res struct {
			Data FileOperation `json:"data"`
		}
		if err := o.call(ctx, "fileOperations.info", map[string]string{"id": id}, &res); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("export %s not ready after %s", id, timeout)
			}
			return err
		}
		switch res.Data.State {
		case "complete":
			return nil
		case "error", "failed", "expired":
			return fmt.Errorf("export %s ended in state %q: %s", id, res.Data.State, res.Data.Error)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("export %s not ready after %s", id, timeout)
			}
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// DownloadExport streams the export zip to a temporary file and returns its
// path. The caller removes it.
func (o *Outline) DownloadExport(ctx context.Context, id string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"id": id})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/api/fileOperations.redirect", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	req.Header.Set("Content-Type", "application/json")

	// The endpoint redirects to the storage URL; net/http follows it and
	// drops the Authorization header if the redirect leaves the Outline host.
	res, err := o.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", &APIError{Status: res.StatusCode, Message: "download failed"}
	}

	f, err := os.CreateTemp("", "outline-export-*.zip")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// DeleteFileOperation removes an export from Outline, including the stored
// zip. Requires an admin token.
func (o *Outline) DeleteFileOperation(ctx context.Context, id string) error {
	return o.call(ctx, "fileOperations.delete", map[string]string{"id": id}, nil)
}

// Exports lists every export file operation in the workspace (admin only).
func (o *Outline) Exports(ctx context.Context) ([]FileOperation, error) {
	var all []FileOperation
	for offset := 0; ; offset += pageSize {
		var res struct {
			Data []FileOperation `json:"data"`
		}
		body := map[string]any{"type": "export", "offset": offset, "limit": pageSize}
		if err := o.call(ctx, "fileOperations.list", body, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Data...)
		if len(res.Data) < pageSize {
			return all, nil
		}
	}
}

// CurrentUserID returns the id of the user that owns the API token.
func (o *Outline) CurrentUserID(ctx context.Context) (string, error) {
	var res struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := o.call(ctx, "auth.info", struct{}{}, &res); err != nil {
		return "", err
	}
	return res.Data.User.ID, nil
}
