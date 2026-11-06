package client

import (
	"net/url"
	"strings"
)

// ProjectPath is /projects/{project}/{collection}[/{id}...], each part escaped.
func ProjectPath(project, collection string, rest ...string) string {
	parts := []string{"projects", url.PathEscape(project), collection}
	for _, r := range rest {
		parts = append(parts, url.PathEscape(r))
	}
	return "/" + strings.Join(parts, "/")
}

// OrgPath is /orgs/{org}/{collection}[/{id}...], each part escaped.
func OrgPath(org, collection string, rest ...string) string {
	parts := []string{"orgs", url.PathEscape(org), collection}
	for _, r := range rest {
		parts = append(parts, url.PathEscape(r))
	}
	return "/" + strings.Join(parts, "/")
}
