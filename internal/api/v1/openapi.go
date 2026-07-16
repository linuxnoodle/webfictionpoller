package v1

// openAPIYAMLJSON is a minimal hand-written OpenAPI 3.0 spec for the v1 API,
// served as JSON at /api/v1/openapi.json. Kept inline so the binary stays
// single-file; regenerated from comments when the surface changes materially.
const openAPIYAMLJSON = `{
  "openapi": "3.0.3",
  "info": {
    "title": "webfiction_poller API",
    "version": "1.0.0",
    "description": "Mobile-ready JSON API. Authenticate with a bearer token (POST /api/v1/auth/login with username+password, then send the returned token as Authorization: Bearer <token>)."
  },
  "servers": [{"url": "/api/v1"}],
  "components": {
    "securitySchemes": {
      "bearerAuth": {"type": "http", "scheme": "bearer"}
    }
  },
  "security": [{"bearerAuth": []}],
  "paths": {
    "/auth/login": {
      "post": {
        "summary": "Exchange username/password for a bearer token",
        "security": [],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"username": {"type": "string"}, "password": {"type": "string"}, "label": {"type": "string"}, "device_id": {"type": "string"}}}}},
        "responses": {"200": {"description": "Token issued", "content": {"application/json": {"schema": {"type": "object", "properties": {"token": {"type": "string"}, "expires_at": {"type": "string", "format": "date-time"}, "user_id": {"type": "integer"}, "username": {"type": "string"}}}}}}}
      }
    },
    "/auth/me": {"get": {"summary": "Current authenticated user"}},
    "/tokens": {
      "get": {"summary": "List bearer tokens for the current user"},
      "post": {"summary": "Issue a new bearer token"}
    },
    "/tokens/{id}": {"delete": {"summary": "Revoke a token", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer"}}]}},
    "/library": {"get": {"summary": "List tracked series", "parameters": [{"name": "kind", "in": "query", "schema": {"type": "string", "enum": ["text", "comic"]}}]}},
    "/library/{id}": {"get": {"summary": "Series detail with chapters", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer"}}, {"name": "kind", "in": "query", "schema": {"type": "string", "enum": ["text", "comic"]}}]}},
    "/chapters": {"get": {"summary": "Recent chapter feed", "parameters": [{"name": "page", "in": "query", "schema": {"type": "integer"}}, {"name": "unread", "in": "query", "schema": {"type": "boolean"}}]}},
    "/chapters/{id}": {"get": {"summary": "Chapter metadata", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer"}}]}},
    "/chapters/{id}/content": {"get": {"summary": "Cached chapter HTML", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer"}}]}},
    "/chapters/{id}/read": {"post": {"summary": "Mark chapter read", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer"}}]}},
    "/unread-count": {"get": {"summary": "Total unread chapters"}},
    "/poll/status": {"get": {"summary": "Current poll-cycle progress"}},
    "/poll/now": {"post": {"summary": "Trigger an immediate poll of every active series"}},
    "/metrics/providers": {"get": {"summary": "Per-provider polling metrics (last-poll, errors, chapter yield)"}},
    "/downloads/comics/{chapterID}": {
      "post": {
        "summary": "Trigger a background download of every page in a comic chapter",
        "parameters": [{"name": "chapterID", "in": "path", "required": true, "schema": {"type": "integer"}}]
      }
    },
    "/downloads/comics/{chapterID}/status": {
      "get": {
        "summary": "Progress of a comic chapter download",
        "parameters": [{"name": "chapterID", "in": "path", "required": true, "schema": {"type": "integer"}}]
      }
    },
    "/downloads/comics/{chapterID}/cbz": {
      "get": {
        "summary": "Stream a CBZ bundle of cached page images for offline reading",
        "parameters": [{"name": "chapterID", "in": "path", "required": true, "schema": {"type": "integer"}}],
        "responses": {"200": {"description": "CBZ file", "content": {"application/vnd.comicbook+zip": {}}}}
      }
    },
    "/providers": {"get": {"summary": "List registered providers (catalog metadata)"}}
  }
}`
