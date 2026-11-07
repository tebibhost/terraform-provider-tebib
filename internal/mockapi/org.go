package mockapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// org handles /orgs/{org}/... : projects and IAM. IAM objects are synchronous: they have no
// lifecycle state and answer 201/200/204 at once.
func (s *Server) org(method string, parts []string, q map[string][]string, body map[string]any) (int, any, *apiErr) {
	op := "/orgs/" + Org
	coll := parts[0]
	switch {
	case coll == "projects" && len(parts) == 1 && method == http.MethodPost:
		name := str(body["name"])
		for p, o := range s.objs {
			if strings.Count(p, "/") == 2 && strings.HasPrefix(p, "/projects/") && o["name"] == name {
				return 0, nil, conflict("A project with this name already exists")
			}
		}
		id := s.id("p")
		s.seedProject(Org, id, name)
		pp := "/projects/" + id
		o := s.objs[pp]
		o["description"], o["tags"], o["state"] = str(body["description"]), tagsOf(body["tags"]), "creating"
		s.settleLater(pp, false, nil)
		return 202, o, nil
	case coll == "projects" && len(parts) == 1 && method == http.MethodGet:
		var items []any
		for p, o := range s.objs {
			if strings.Count(p, "/") == 2 && strings.HasPrefix(p, "/projects/") {
				items = append(items, o)
			}
		}
		return 200, listPage(items), nil
	case coll == "service-accounts" && len(parts) == 3 && parts[2] == "access-keys":
		return s.accessKeys(method, op, "service-accounts", parts[1], "", body)
	case coll == "service-accounts" && len(parts) == 4 && parts[2] == "access-keys":
		return s.accessKeys(method, op, "service-accounts", parts[1], parts[3], body)
	case coll == "users" && len(parts) >= 3 && parts[2] == "access-keys":
		key := ""
		if len(parts) == 4 {
			key = parts[3]
		}
		return s.accessKeys(method, op, "users", parts[1], key, body)
	case coll == "groups" && len(parts) >= 3 && parts[2] == "members":
		return s.members(method, op, parts[1], parts[3:])
	case coll == "policies" && len(parts) >= 3 && parts[2] == "versions":
		return s.versions(method, op, parts[1], parts[3:], body)
	case coll == "policy-attachments":
		return s.attachments(method, op, parts[1:], q, body)
	case coll == "service-accounts" || coll == "roles" || coll == "policies" || coll == "groups":
		return s.iamObject(method, op, coll, parts[1:], body)
	}
	return 0, nil, notFound("Route")
}

var iamPrefixes = map[string]string{
	"service-accounts": "sa", "roles": "role", "policies": "pol", "groups": "grp",
}

var iamTypes = map[string]string{
	"service-accounts": "service-account", "roles": "role", "policies": "policy", "groups": "group",
}

func (s *Server) iamObject(method, op, coll string, rest []string, body map[string]any) (int, any, *apiErr) {
	cp := op + "/" + coll
	if len(rest) == 0 {
		switch method {
		case http.MethodPost:
			name := str(body["name"])
			if s.nameTaken(cp, name) {
				return 0, nil, conflict(fmt.Sprintf("%q already exists", name))
			}
			id := s.id(iamPrefixes[coll])
			o := map[string]any{
				"id": id, "trn": fmt.Sprintf("trn:tebib:iam::%s:%s/%s", Org, iamTypes[coll], name),
				"name": name, "description": str(body["description"]), "created_at": now(), "updated_at": now(),
			}
			switch coll {
			case "service-accounts":
				o["tags"], o["disabled"] = tagsOf(body["tags"]), false
			case "roles":
				o["tags"], o["trust_policy"] = tagsOf(body["tags"]), body["trust_policy"]
				o["max_session_seconds"] = body["max_session_seconds"]
				if o["max_session_seconds"] == nil {
					o["max_session_seconds"] = 3600
				}
			case "policies":
				o["document"], o["default_version"], o["managed"], o["attachment_count"] = body["document"], 1, false, 0
				o["versions"] = []any{map[string]any{"version": 1, "document": body["document"], "is_default": true,
					"created_at": now(), "created_by": "test"}}
				o["last_version"] = 1
			case "groups":
				o["member_count"], o["members"] = 0, []any{}
			}
			s.put(cp+"/"+id, o)
			return 201, public(o), nil
		case http.MethodGet:
			var items []any
			for _, p := range s.sortedPaths(cp) {
				items = append(items, public(s.objs[p]))
			}
			return 200, listPage(items), nil
		}
		return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
	}
	ip := cp + "/" + rest[0]
	o, ok := s.objs[ip]
	if !ok {
		return 0, nil, notFound(iamTypes[coll])
	}
	switch method {
	case http.MethodGet:
		return 200, public(o), nil
	case http.MethodPatch:
		for _, k := range []string{"description", "tags", "disabled", "trust_policy", "max_session_seconds"} {
			if v, ok := body[k]; ok && v != nil {
				o[k] = v
			}
		}
		o["updated_at"] = now()
		return 200, public(o), nil
	case http.MethodDelete:
		if coll == "policies" && toFloat(o["attachment_count"]) > 0 {
			return 0, nil, conflict("Detach the policy from every principal before deleting it")
		}
		delete(s.objs, ip)
		return 204, nil, nil
	}
	return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
}

// public drops the fake's internal fields.
func public(o map[string]any) map[string]any {
	out := clone(o)
	for _, k := range []string{"versions", "last_version", "members", "secret_hash", "owner_kind", "owner_id"} {
		delete(out, k)
	}
	return out
}

func (s *Server) accessKeys(method, op, kind, owner, keyID string, body map[string]any) (int, any, *apiErr) {
	ownerTRN := ""
	if kind == "service-accounts" {
		sa := s.objs[op+"/service-accounts/"+owner]
		if sa == nil {
			return 0, nil, notFound("Service account")
		}
		ownerTRN = str(sa["trn"])
	} else {
		ownerTRN = fmt.Sprintf("trn:tebib:iam::%s:user/%s", Org, owner)
	}
	cp := op + "/access-keys"
	switch {
	case keyID == "" && method == http.MethodPost:
		id := fmt.Sprintf("TBKMOCK%04d", s.seq+1)
		s.seq++
		expires := body["expires_at"]
		if body["never_expires"] == true {
			if kind != "service-accounts" {
				return 0, nil, fail(400, "InvalidRequest", "only service-account keys can be made never to expire")
			}
			expires = nil
		} else if expires == nil {
			expires = "2027-02-04T00:00:00+00:00"
		} else {
			// The API answers in its own spelling of the same instant.
			expires = strings.Replace(str(expires), "Z", "+00:00", 1)
		}
		cidrs, _ := body["allowed_cidrs"].([]any)
		if cidrs == nil {
			cidrs = []any{}
		}
		o := map[string]any{
			"id": id, "trn": fmt.Sprintf("trn:tebib:iam::%s:access-key/%s", Org, id), "owner": ownerTRN,
			"description": str(body["description"]), "status": "active", "status_reason": "",
			"expires_at": expires, "allowed_cidrs": cidrs, "created_at": now(), "last_used_at": nil,
			"last_used_ip": nil, "disables_at": nil, "owner_kind": kind, "owner_id": owner,
		}
		s.put(cp+"/"+id, o)
		out := public(o)
		out["secret"] = "mocksecret-" + id
		out["token"] = id + ".mocksecret-" + id
		return 201, out, nil
	case keyID == "" && method == http.MethodGet:
		items := []any{}
		for _, p := range s.sortedPaths(cp) {
			o := s.objs[p]
			if o["owner_kind"] == kind && o["owner_id"] == owner {
				items = append(items, public(o))
			}
		}
		return 200, items, nil
	}
	o := s.objs[cp+"/"+keyID]
	if o == nil || o["owner_kind"] != kind || o["owner_id"] != owner {
		return 0, nil, notFound("Access key")
	}
	switch method {
	case http.MethodPatch:
		for _, k := range []string{"description", "status"} {
			if v, ok := body[k]; ok && v != nil {
				o[k] = v
			}
		}
		return 200, public(o), nil
	case http.MethodDelete:
		delete(s.objs, cp+"/"+keyID)
		return 204, nil, nil
	}
	return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
}

func (s *Server) versions(method, op, policyID string, rest []string, body map[string]any) (int, any, *apiErr) {
	o := s.objs[op+"/policies/"+policyID]
	if o == nil {
		return 0, nil, notFound("Policy")
	}
	versions, _ := o["versions"].([]any)
	switch {
	case len(rest) == 0 && method == http.MethodGet:
		return 200, versions, nil
	case len(rest) == 0 && method == http.MethodPost:
		if len(versions) >= 5 {
			return 0, nil, conflict("A policy keeps at most 5 versions; delete one first")
		}
		n := int(toFloat(o["last_version"])) + 1
		o["last_version"] = n
		def := body["set_as_default"] != false
		v := map[string]any{"version": n, "document": body["document"], "is_default": def, "created_at": now(), "created_by": "test"}
		if def {
			for _, x := range versions {
				x.(map[string]any)["is_default"] = false
			}
			o["default_version"], o["document"] = n, body["document"]
		}
		o["versions"] = append(versions, v)
		return 201, v, nil
	case len(rest) == 1 && method == http.MethodDelete:
		n, _ := strconv.Atoi(rest[0])
		if n == int(toFloat(o["default_version"])) {
			return 0, nil, conflict("The default version can't be deleted; set another default first")
		}
		var keep []any
		found := false
		for _, x := range versions {
			if int(toFloat(x.(map[string]any)["version"])) == n {
				found = true
				continue
			}
			keep = append(keep, x)
		}
		if !found {
			return 0, nil, notFound("Policy version")
		}
		o["versions"] = keep
		return 204, nil, nil
	}
	return 0, nil, notFound("Route")
}

func (s *Server) attachments(method, op string, rest []string, q map[string][]string, body map[string]any) (int, any, *apiErr) {
	cp := op + "/policy-attachments"
	resolvePolicy := func(ref string) map[string]any {
		for _, p := range s.sortedPaths(op + "/policies") {
			o := s.objs[p]
			if o["id"] == ref || o["trn"] == ref || o["name"] == ref {
				return o
			}
		}
		return nil
	}
	switch {
	case len(rest) == 0 && method == http.MethodPost:
		pol := resolvePolicy(str(body["policy"]))
		if pol == nil {
			return 0, nil, notFound("Policy")
		}
		principal := str(body["principal"])
		kind := ""
		for _, k := range []string{"service-account", "role", "group", "user"} {
			if strings.Contains(principal, ":"+k+"/") {
				kind = k // the API spells it service-account
			}
		}
		if kind == "" {
			return 0, nil, fail(400, "InvalidRequest", "principal: not a user, group, service account or role TRN")
		}
		id := s.id("pat")
		o := map[string]any{
			"id": id, "policy_id": pol["id"], "policy_trn": pol["trn"], "principal": principal,
			"principal_type": kind, "project_id": body["project_id"], "created_at": now(), "created_by": "test",
		}
		pol["attachment_count"] = toFloat(pol["attachment_count"]) + 1
		s.put(cp+"/"+id, o)
		return 201, o, nil
	case len(rest) == 0 && method == http.MethodGet:
		var items []any
		var polID string
		if v := q["policy"]; len(v) > 0 && v[0] != "" {
			if pol := resolvePolicy(v[0]); pol != nil {
				polID = str(pol["id"])
			} else {
				return 0, nil, notFound("Policy")
			}
		}
		for _, p := range s.sortedPaths(cp) {
			if polID == "" || s.objs[p]["policy_id"] == polID {
				items = append(items, s.objs[p])
			}
		}
		return 200, listPage(items), nil
	case len(rest) == 1 && method == http.MethodDelete:
		o := s.objs[cp+"/"+rest[0]]
		if o == nil {
			return 0, nil, notFound("Policy attachment")
		}
		if pol := s.objs[op+"/policies/"+str(o["policy_id"])]; pol != nil {
			pol["attachment_count"] = toFloat(pol["attachment_count"]) - 1
		}
		delete(s.objs, cp+"/"+rest[0])
		return 204, nil, nil
	}
	return 0, nil, notFound("Route")
}

func (s *Server) members(method, op, groupID string, rest []string) (int, any, *apiErr) {
	g := s.objs[op+"/groups/"+groupID]
	if g == nil {
		return 0, nil, notFound("Group")
	}
	members, _ := g["members"].([]any)
	if len(rest) == 0 && method == http.MethodGet {
		if members == nil {
			members = []any{}
		}
		return 200, members, nil
	}
	if len(rest) != 1 {
		return 0, nil, notFound("Route")
	}
	user := rest[0]
	var keep []any
	present := false
	for _, m := range members {
		if m.(map[string]any)["user_id"] == user {
			present = true
			continue
		}
		keep = append(keep, m)
	}
	switch method {
	case http.MethodPut:
		if !present {
			keep = append(keep, map[string]any{
				"user_id": user, "email": user + "@example.com",
				"trn": fmt.Sprintf("trn:tebib:iam::%s:user/%s", Org, user), "added_at": now(),
			})
		} else {
			keep = members
		}
	case http.MethodDelete:
		if !present {
			return 0, nil, notFound("Member")
		}
	default:
		return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
	}
	sort.SliceStable(keep, func(i, j int) bool {
		return str(keep[i].(map[string]any)["user_id"]) < str(keep[j].(map[string]any)["user_id"])
	})
	if keep == nil {
		keep = []any{}
	}
	g["members"], g["member_count"] = keep, len(keep)
	return 204, nil, nil
}
