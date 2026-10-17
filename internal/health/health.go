// Package health waits for AnsibleForms to answer, after a start or an upgrade.
package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Wait polls the app's version endpoint (it needs no sign-in, as the image's own health
// check) until it answers, or the time is up.
//
// Args:
//
//	ctx: the context, to cancel.
//	baseURL: the app's address (https://localhost:443).
//	timeout: how long to wait.
//
// Returns:
//
//	The version the app reports.
//
// Errors:
//
//	When it has not answered in time.
func Wait(ctx context.Context, baseURL string, timeout time.Duration) (string, error) {
	// a fresh install serves a self-signed certificate : the check only asks whether it answers
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // local health check
	}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		v, err := version(ctx, client, baseURL)
		if err == nil {
			return v, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return "", fmt.Errorf("AnsibleForms did not answer on %s within %s: %v", baseURL, timeout, last)
}

// version asks the app its version once.
//
// Args:
//
//	ctx: the context, to cancel.
//	client: the HTTP client.
//	baseURL: the app's address.
//
// Returns:
//
//	The version.
//
// Errors:
//
//	When it does not answer 200 with a version.
func version(ctx context.Context, client *http.Client, baseURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v2/version", nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("answered %s", res.Status)
	}
	var body struct {
		Version string `json:"version"`
		Data    struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.Version != "" {
		return body.Version, nil
	}
	return body.Data.Output, nil
}

// AppVersion asks an app its version once, its certificate checked : what a runner installs.
//
// Args:
//
//	ctx: the context, to cancel.
//	baseURL: the app's address.
//
// Returns:
//
//	The version ; whether the certificate was accepted (false : the error is a certificate one,
//	a self-signed app the person may choose to trust) ; the error.
func AppVersion(ctx context.Context, baseURL string) (string, bool, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	v, err := version(ctx, client, baseURL)
	if err != nil {
		var unknown x509.UnknownAuthorityError
		var invalid x509.CertificateInvalidError
		var host x509.HostnameError
		if errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &host) || strings.Contains(err.Error(), "certificate") {
			return "", false, err
		}
		return "", true, err
	}
	return v, true, nil
}

// AppVersionInsecure asks an app its version without checking its certificate (one the person
// chose to trust).
//
// Args:
//
//	ctx: the context, to cancel.
//	baseURL: the app's address.
//
// Returns:
//
//	The version, true, and the error.
func AppVersionInsecure(ctx context.Context, baseURL string) (string, bool, error) {
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // the person trusts this app's certificate
	}
	v, err := version(ctx, client, baseURL)
	return v, true, err
}
