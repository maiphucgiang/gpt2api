package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

var errExternalPending = errors.New("external credentials are awaiting an update from their source")

func bindExternalCredential(account map[string]any, owner, source, remote string) map[string]any {
	account["credential_owner"] = owner
	account["credential_source_id"] = source
	account["credential_remote_id"] = remote
	subject, workspace := accountpolicy.Identity(account)
	account["credential_subject"], account["credential_workspace_id"] = subject, workspace
	delete(account, "refresh_token")
	return account
}

func (s *Server) readCPACredential(ctx context.Context, pool externalCPAPool, name string) (map[string]any, error) {
	payload, err := fetchCPACredential(ctx, pool, name)
	current, ok := s.external.getCPA(pool.ID)
	if !ok || current.BaseURL != pool.BaseURL || current.SecretKey != pool.SecretKey {
		return nil, errors.New("CPA source connection changed during synchronization")
	}
	return payload, err
}

func (s *Server) readSubCredential(ctx context.Context, server externalSubServer, id string, headers map[string]string) (map[string]any, error) {
	payload, err := fetchSubCredential(ctx, server, id, headers)
	current, ok := s.external.getSub(server.ID)
	if !ok || current.BaseURL != server.BaseURL || current.APIKey != server.APIKey || current.Email != server.Email || current.Password != server.Password || current.VerifyTLS != server.VerifyTLS {
		return nil, errors.New("Sub2API source connection changed during synchronization")
	}
	return payload, err
}

func fetchCPACredential(ctx context.Context, pool externalCPAPool, name string) (map[string]any, error) {
	payload, _, err := remoteJSONContext(ctx, remoteClient(true), http.MethodGet, pool.BaseURL+"/v0/management/auth-files/download", map[string]string{"Authorization": "Bearer " + pool.SecretKey, "Accept": "application/json"}, url.Values{"name": []string{name}}, nil)
	if err != nil {
		return nil, errors.New("CPA credential download failed")
	}
	account, err := normalizeCPACredential(payload)
	if err != nil {
		return nil, err
	}
	return bindExternalCredential(account, "cpa", pool.ID, name), nil
}

func subHeadersContext(ctx context.Context, item externalSubServer) (map[string]string, error) {
	if strings.TrimSpace(item.APIKey) != "" {
		return map[string]string{"x-api-key": item.APIKey, "Accept": "application/json"}, nil
	}
	payload, _, err := remoteJSONContext(ctx, remoteClient(item.VerifyTLS), http.MethodPost, item.BaseURL+"/api/v1/auth/login", map[string]string{"Accept": "application/json"}, nil, map[string]string{"email": item.Email, "password": item.Password})
	if err != nil {
		return nil, errors.New("Sub2API management login failed")
	}
	token := recursiveString(payload, "access_token", "accessToken", "token")
	if token == "" {
		return nil, errors.New("Sub2API management login returned no token")
	}
	return map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json"}, nil
}

func fetchSubCredential(ctx context.Context, server externalSubServer, id string, headers map[string]string) (map[string]any, error) {
	payload, _, err := remoteJSONContext(ctx, remoteClient(server.VerifyTLS), http.MethodGet, server.BaseURL+"/api/v1/admin/accounts/data", headers, url.Values{"ids": []string{id}, "include_proxies": []string{"false"}}, nil)
	if err != nil {
		return nil, errors.New("Sub2API credential export failed")
	}
	items := pagedValues(payload, "accounts", "items", "list", "records", "rows")
	if len(items) != 1 {
		return nil, errors.New("Sub2API export must contain exactly the selected account")
	}
	item := mapValue(items[0])
	if strings.ToLower(stringValue(item["platform"])) != "openai" || strings.ToLower(stringValue(item["type"])) != "oauth" {
		return nil, errUnsupportedCPACredential
	}
	if actual := stringValue(item["id"]); actual != "" && actual != id {
		return nil, store.ErrExternalIdentity
	}
	credentials := cloneMap(nestedMap(item, "credentials", "credential"))
	credentials["type"] = "codex"
	delete(credentials, "provider")
	account, err := normalizeCPACredential(credentials)
	if err != nil {
		return nil, err
	}
	if email := stringValue(account["email"]); email == "" {
		account["email"] = stringValue(item["email"])
	}
	return bindExternalCredential(account, "sub2api", server.ID, id), nil
}

// syncExternalCredentials never calls an OAuth token endpoint.
func (s *Server) syncExternalCredentials(ctx context.Context, account map[string]any, rejected bool) (map[string]any, error) {
	version := store.CredentialsOf(account)
	owner := accountpolicy.Owner(account)
	source, remote := stringValue(account["credential_source_id"]), stringValue(account["credential_remote_id"])
	var payload map[string]any
	var err error
	retry, _ := time.Parse(time.RFC3339, stringValue(account["credential_sync_retry_at"]))
	if retry.After(time.Now()) {
		return nil, errExternalPending
	}
	if source == "" || remote == "" {
		err = errors.New("external credential source is not bound")
	} else {
		switch owner {
		case "cpa":
			if pool, ok := s.external.getCPA(source); ok {
				payload, err = s.readCPACredential(ctx, pool, remote)
			} else {
				err = errors.New("CPA source connection no longer exists")
			}
		case "sub2api":
			if server, ok := s.external.getSub(source); ok {
				var headers map[string]string
				headers, err = subHeadersContext(ctx, server)
				if err == nil {
					payload, err = s.readSubCredential(ctx, server, remote, headers)
				}
			} else {
				err = errors.New("Sub2API source connection no longer exists")
			}
		default:
			err = errors.New("external credential source is not bound")
		}
	}
	if err == nil {
		if !store.ExternalIdentityMatches(account, payload) {
			err = store.ErrExternalIdentity
		} else if expires := accountpolicy.Expiry(payload); (!expires.IsZero() && !expires.After(time.Now().Add(30*time.Second))) || ((rejected || accountpolicy.Due(account, time.Now())) && accountToken(payload) == accountToken(account)) {
			err = errExternalPending
		}
	}
	if err == nil {
		return s.store.SaveExternalCredentials(version, account, payload)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	failures := min(intValue(account["credential_sync_failures"])+1, 7)
	updates := map[string]any{"credential_sync_status": "pending", "credential_sync_error": err.Error(), "credential_sync_failures": failures,
		"credential_sync_retry_at": time.Now().UTC().Add(time.Minute * time.Duration(1<<uint(failures-1))).Format(time.RFC3339)}
	if _, saveErr := s.store.UpdateAccountIfCredentialsMatch(version, updates); saveErr != nil {
		return nil, saveErr
	}
	return nil, err
}
