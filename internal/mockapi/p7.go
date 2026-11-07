package mockapi

import (
	"fmt"
	"net/http"
	"strings"
)

// Phase 7 endpoints: buckets, storage keys, load balancers, DNS zones and records, and
// snapshot schedules. Like the rest of the fake they keep JSON objects and settle after a
// few GETs; only the rules the provider relies on are checked.

// VerifyZone plays the domain being delegated to us: the zone becomes active and served.
func (s *Server) VerifyZone(zonePath string) {
	s.Mutate(zonePath, func(o map[string]any) {
		o["state"], o["status"], o["served"], o["serial"], o["delegation_ok"] = "active", "active", true, 2026110701, true
		v := o["verification"].(map[string]any)
		v["state"], v["method"] = "verified", "ns"
	})
}

// SetObjects pretends a bucket holds objects (the real count comes from Ceph).
func (s *Server) SetObjects(bucketPath string, n int) {
	s.Mutate(bucketPath, func(o map[string]any) { o["object_count"] = n })
}

const s3Endpoint = "https://s3.lab-neo-1.example"

var p7Collections = map[string]bool{
	"storage": true, "buckets": true, "storage-keys": true, "load-balancers": true,
	"snapshot-schedules": true, "dns": true,
}

// p7 handles the Phase 7 collections; ok is false for anything else.
func (s *Server) p7(method, project string, parts []string, q map[string][]string, body map[string]any) (ok bool, status int, out any, e *apiErr) {
	if !p7Collections[parts[0]] {
		return false, 0, nil, nil
	}
	pp := "/projects/" + project
	switch parts[0] {
	case "storage":
		if method != http.MethodGet || len(parts) != 1 {
			return true, 0, nil, notFound("Route")
		}
		return true, 200, map[string]any{
			"project_id": project, "endpoint": s3Endpoint, "region": "lab-neo-1",
			"buckets": len(s.sortedPaths(pp + "/buckets")), "access_keys": len(s.sortedPaths(pp + "/storage-keys")),
			"stored_bytes": 0, "quota_gib": 100, "state": "active",
		}, nil
	case "dns":
		if len(parts) < 2 || parts[1] != "zones" {
			return true, 0, nil, notFound("Route")
		}
		status, out, e := s.dns(method, project, parts[2:], body)
		return true, status, out, e
	}
	coll := parts[0]
	cp := pp + "/" + coll
	if len(parts) == 1 {
		switch method {
		case http.MethodGet:
			var items []any
			for _, p := range s.sortedPaths(cp) {
				if o, err := s.get(p); err == nil && matchQuery(o, q) {
					items = append(items, o)
				}
			}
			return true, 200, listPage(items), nil
		case http.MethodPost:
			status, out, e := s.createP7(project, coll, body)
			return true, status, out, e
		}
		return true, 0, nil, fail(405, "InvalidRequest", "Method not allowed")
	}
	ip := cp + "/" + parts[1]
	o, found := s.objs[ip]
	if !found {
		return true, 0, nil, notFound("Resource")
	}
	if len(parts) > 2 {
		if coll != "load-balancers" {
			return true, 0, nil, notFound("Route")
		}
		status, out, e := s.lbPart(method, pp, ip, o, parts[2:], body)
		return true, status, out, e
	}
	switch method {
	case http.MethodGet:
		got, err := s.get(ip)
		if err != nil {
			return true, 0, nil, err
		}
		if coll == "storage-keys" {
			got = withoutSecret(got)
		}
		return true, 200, got, nil
	case http.MethodPatch:
		if o["state"] == "deleting" {
			return true, 0, nil, conflict("It is being deleted")
		}
		if err := s.patchP7(coll, o, body); err != nil {
			return true, 0, nil, err
		}
		o["state"] = "updating"
		s.settleLater(ip, false, nil)
		if coll == "storage-keys" {
			return true, 200, withoutSecret(o), nil
		}
		return true, 200, o, nil
	case http.MethodDelete:
		if coll == "buckets" && toFloat(o["object_count"]) > 0 && strings.Join(q["force"], "") != "true" {
			return true, 0, nil, fail(409, "Conflict", fmt.Sprintf(
				"The bucket holds %v object(s): delete them first, or delete the bucket with force", o["object_count"]))
		}
		o["state"] = "deleting"
		s.settleLater(ip, true, nil)
		return true, 202, o, nil
	}
	return true, 0, nil, fail(405, "InvalidRequest", "Method not allowed")
}

func withoutSecret(o map[string]any) map[string]any {
	c := clone(o)
	delete(c, "secret_access_key")
	delete(c, "region")
	return c
}

var p7Prefixes = map[string]string{
	"buckets": "bkt", "storage-keys": "s3key", "load-balancers": "lb", "snapshot-schedules": "ssch",
}

var p7Services = map[string]string{
	"buckets": "storage", "storage-keys": "storage", "load-balancers": "network", "snapshot-schedules": "compute",
}

func (s *Server) createP7(project, coll string, body map[string]any) (int, any, *apiErr) {
	pp := "/projects/" + project
	cp := pp + "/" + coll
	name := str(body["name"])
	if name == "" {
		return 0, nil, fail(422, "InvalidRequest", "name: field required")
	}
	if s.nameTaken(cp, name) {
		return 0, nil, conflict(fmt.Sprintf("A resource named %q already exists", name))
	}
	id := s.id(p7Prefixes[coll])
	o := meta(id, project, name, p7Services[coll], body)
	var apply map[string]any
	switch coll {
	case "buckets":
		vis := str(body["visibility"])
		if vis == "" {
			vis = "private"
		}
		o["visibility"], o["max_size_gib"] = vis, body["max_size_gib"]
		o["stored_bytes"], o["object_count"] = 0, 0
		o["endpoint"], o["url"] = s3Endpoint, s3Endpoint+"/"+name
		o["observed_at"] = nil
	case "storage-keys":
		holder := "trn:tebib:iam::" + Org + ":user/u-test"
		if sa := str(body["service_account_id"]); sa != "" {
			obj := s.objs["/orgs/"+Org+"/service-accounts/"+sa]
			if obj == nil {
				return 0, nil, notFound("Service account " + sa)
			}
			holder = str(obj["trn"])
		}
		o["access_key_id"] = fmt.Sprintf("TBOMOCK%06d", s.seq)
		o["holder"], o["status"], o["effective_status"] = holder, "active", "pending"
		o["previous_access_key_id"], o["previous_expires_at"], o["rotated_at"] = nil, nil, nil
		o["endpoint"] = s3Endpoint
		apply = map[string]any{"effective_status": "active"}
		out := clone(o)
		out["secret_access_key"], out["region"] = "mock-secret-"+id, "lab-neo-1"
		s.put(cp+"/"+id, o)
		s.settleLater(cp+"/"+id, false, apply)
		return 202, out, nil
	case "load-balancers":
		scheme := str(body["scheme"])
		if scheme == "" {
			scheme = "public"
		}
		subnetID := str(body["subnet_id"])
		if subnetID == "" {
			if def := s.objs[s.findDefault(pp+"/subnets")]; def != nil {
				subnetID = str(def["id"])
			}
		}
		subnet := s.objs[pp+"/subnets/"+subnetID]
		if subnet == nil {
			return 0, nil, fail(404, "NotFound", "Subnet "+subnetID+" not found")
		}
		sgs, _ := body["security_group_ids"].([]any)
		if len(sgs) == 0 {
			for _, p := range s.sortedPaths(pp + "/security-groups") {
				if s.objs[p]["vpc_id"] == subnet["vpc_id"] && s.objs[p]["is_default"] == true {
					sgs = []any{s.objs[p]["id"]}
				}
			}
		}
		o["scheme"], o["vpc_id"], o["subnet_id"], o["security_group_ids"] = scheme, subnet["vpc_id"], subnetID, sgs
		o["address"], o["public_ip_id"], o["public_address"] = "100.68.0.10", nil, nil
		if scheme == "public" {
			if ipID := str(body["public_ip_id"]); ipID != "" {
				ipObj := s.objs[pp+"/public-ips/"+ipID]
				if ipObj == nil {
					return 0, nil, fail(404, "NotFound", "Public IP "+ipID+" not found")
				}
				if ipObj["association"] != "none" {
					return 0, nil, conflict("The public IP is associated")
				}
				ipObj["association"] = "load-balancer"
				o["public_ip_id"], o["public_address"] = ipID, ipObj["address"]
			} else {
				o["public_address"] = fmt.Sprintf("203.0.113.%d", s.seq%250+1)
			}
		}
		o["nodes"] = map[string]any{"desired": 2, "ready": 0}
		o["listeners"], o["target_groups"] = []any{}, []any{}
		apply = map[string]any{"nodes": map[string]any{"desired": 2, "ready": 2}}
	case "snapshot-schedules":
		if s.objs[pp+"/vms/"+str(body["vm_id"])] == nil {
			return 0, nil, fail(404, "NotFound", "VM "+str(body["vm_id"])+" not found")
		}
		if body["retention_count"] == nil && body["retention_days"] == nil {
			return 0, nil, fail(400, "InvalidRequest", "retention_count: set retention_count, retention_days or both")
		}
		o["vm_id"] = body["vm_id"]
		if e := setTiming(o, body); e != nil {
			return 0, nil, e
		}
		o["retention_count"], o["retention_days"] = body["retention_count"], body["retention_days"]
		o["enabled"] = body["enabled"] != false
		o["next_run_at"], o["last_run"] = "2030-01-01T00:00:00Z", nil
		o["failures"], o["missed"], o["snapshot_count"] = 0, 0, 0
	}
	s.put(cp+"/"+id, o)
	s.settleLater(cp+"/"+id, false, apply)
	return 202, o, nil
}

// setTiming sets frequency, cron and timezone the way the API does (presets become cron).
func setTiming(o, body map[string]any) *apiErr {
	get := func(k string, def any) any {
		if v, ok := body[k]; ok && v != nil {
			return v
		}
		return def
	}
	freq := str(get("frequency", o["frequency"]))
	minute, hour, weekday := get("minute", 0), get("hour", 0), get("weekday", 0)
	cron := str(get("cron", ""))
	if freq == "custom" && cron == "" {
		cron = str(o["cron"])
	}
	switch freq {
	case "hourly":
		cron = fmt.Sprintf("%v * * * *", minute)
	case "daily":
		cron = fmt.Sprintf("%v %v * * *", minute, hour)
	case "weekly":
		cron = fmt.Sprintf("%v %v * * %v", minute, hour, weekday)
	case "custom":
		if cron == "" {
			return fail(400, "InvalidRequest", "cron: required when frequency is custom")
		}
	default:
		return fail(422, "InvalidRequest", "frequency: unknown")
	}
	if freq != "custom" && str(body["cron"]) != "" {
		return fail(400, "InvalidRequest", "cron: only with frequency custom")
	}
	o["frequency"], o["cron"] = freq, cron
	o["timezone"] = get("timezone", get("tz", o["timezone"]))
	if o["timezone"] == nil || o["timezone"] == "" {
		o["timezone"] = "UTC"
	}
	return nil
}

func (s *Server) patchP7(coll string, o, body map[string]any) *apiErr {
	for _, k := range []string{"description", "tags"} {
		if v, ok := body[k]; ok && v != nil {
			o[k] = v
		}
	}
	switch coll {
	case "buckets":
		if v := body["visibility"]; v != nil {
			o["visibility"] = v
		}
		if v := body["max_size_gib"]; v != nil {
			if toFloat(v) == 0 {
				o["max_size_gib"] = nil
			} else {
				o["max_size_gib"] = v
			}
		}
	case "storage-keys":
		if v := body["status"]; v != nil {
			o["status"], o["effective_status"] = v, v
		}
	case "snapshot-schedules":
		for _, k := range []string{"frequency", "minute", "hour", "weekday", "cron", "timezone"} {
			if body[k] != nil {
				if e := setTiming(o, body); e != nil {
					return e
				}
				break
			}
		}
		count, days := o["retention_count"], o["retention_days"]
		if body["clear_retention_count"] == true {
			count = nil
		}
		if body["clear_retention_days"] == true {
			days = nil
		}
		if body["retention_count"] != nil {
			count = body["retention_count"]
		}
		if body["retention_days"] != nil {
			days = body["retention_days"]
		}
		if count == nil && days == nil {
			return fail(400, "InvalidRequest", "retention_count: set retention_count, retention_days or both")
		}
		o["retention_count"], o["retention_days"] = count, days
		if v, ok := body["enabled"].(bool); ok {
			o["enabled"] = v
			if !v {
				o["next_run_at"] = nil
			} else {
				o["next_run_at"] = "2030-01-01T00:00:00Z"
			}
		}
	}
	return nil
}

// -- load balancer parts --------------------------------------------------------------------

func lbList(o map[string]any, key string) []any {
	l, _ := o[key].([]any)
	return l
}

func (s *Server) lbChanged(ip string, o map[string]any) (int, any, *apiErr) {
	o["state"] = "updating"
	s.settleLater(ip, false, nil)
	return 202, o, nil
}

func (s *Server) lbPart(method, pp, ip string, o map[string]any, rest []string, body map[string]any) (int, any, *apiErr) {
	if o["state"] == "deleting" {
		return 0, nil, conflict("The load balancer is being deleted")
	}
	switch {
	case rest[0] == "security-groups" && method == http.MethodPut && len(rest) == 1:
		o["security_group_ids"] = body["security_group_ids"]
		return s.lbChanged(ip, o)
	case rest[0] == "target-groups" && method == http.MethodPost && len(rest) == 1:
		for _, g := range lbList(o, "target_groups") {
			if g.(map[string]any)["name"] == body["name"] {
				return 0, nil, conflict(fmt.Sprintf("A target group named %q already exists", body["name"]))
			}
		}
		g := map[string]any{
			"id": s.id("lbtg"), "name": body["name"], "protocol": orDefault(body["protocol"], "tcp"),
			"port": body["port"], "algorithm": orDefault(body["algorithm"], "round_robin"),
			"proxy_protocol": body["proxy_protocol"] == true, "health_check": healthCheck(body["health_check"]),
			"targets": []any{}, "created_at": now(),
		}
		o["target_groups"] = append(lbList(o, "target_groups"), g)
		return s.lbChanged(ip, o)
	case rest[0] == "target-groups" && len(rest) >= 2:
		var g map[string]any
		idx := -1
		for i, x := range lbList(o, "target_groups") {
			if x.(map[string]any)["id"] == rest[1] {
				g, idx = x.(map[string]any), i
			}
		}
		if g == nil {
			return 0, nil, notFound("Target group")
		}
		switch {
		case len(rest) == 2 && method == http.MethodPatch:
			for _, k := range []string{"port", "algorithm", "proxy_protocol"} {
				if v, ok := body[k]; ok && v != nil {
					g[k] = v
				}
			}
			if v := body["health_check"]; v != nil {
				g["health_check"] = healthCheck(v)
			}
			return s.lbChanged(ip, o)
		case len(rest) == 2 && method == http.MethodDelete:
			for _, l := range lbList(o, "listeners") {
				if l.(map[string]any)["target_group_id"] == rest[1] {
					return 0, nil, conflict("Listeners forward to this target group: " + str(l.(map[string]any)["id"]))
				}
			}
			groups := lbList(o, "target_groups")
			o["target_groups"] = append(append([]any{}, groups[:idx]...), groups[idx+1:]...)
			return s.lbChanged(ip, o)
		case len(rest) == 3 && rest[2] == "targets" && method == http.MethodPost:
			vm := s.objs[pp+"/vms/"+str(body["vm_id"])]
			if vm == nil {
				return 0, nil, fail(404, "NotFound", "VM "+str(body["vm_id"])+" not found")
			}
			for _, t := range lbList(g, "targets") {
				tm := t.(map[string]any)
				if tm["vm_id"] == body["vm_id"] && str(tm["port"]) == str(body["port"]) {
					return 0, nil, conflict("VM is already a target of the group on that port")
				}
			}
			port := body["port"]
			eff := port
			if eff == nil {
				eff = g["port"]
			}
			health := "unused"
			for _, l := range lbList(o, "listeners") {
				if l.(map[string]any)["target_group_id"] == g["id"] {
					health = "initial"
				}
			}
			g["targets"] = append(lbList(g, "targets"), map[string]any{
				"id": s.id("lbt"), "vm_id": body["vm_id"], "port": port, "effective_port": eff,
				"health": health, "health_reason": "", "created_at": now(),
			})
			return s.lbChanged(ip, o)
		case len(rest) == 4 && rest[2] == "targets" && method == http.MethodDelete:
			var keep []any
			found := false
			for _, t := range lbList(g, "targets") {
				if t.(map[string]any)["id"] == rest[3] {
					found = true
					continue
				}
				keep = append(keep, t)
			}
			if !found {
				return 0, nil, notFound("Target")
			}
			if keep == nil {
				keep = []any{}
			}
			g["targets"] = keep
			return s.lbChanged(ip, o)
		}
	case rest[0] == "listeners" && method == http.MethodPost && len(rest) == 1:
		for _, l := range lbList(o, "listeners") {
			if toFloat(l.(map[string]any)["port"]) == toFloat(body["port"]) {
				return 0, nil, conflict(fmt.Sprintf("Port %v already has a listener", body["port"]))
			}
		}
		if !hasGroup(o, body["target_group_id"]) {
			return 0, nil, fail(404, "NotFound", "Target group "+str(body["target_group_id"])+" not found")
		}
		o["listeners"] = append(lbList(o, "listeners"), map[string]any{
			"id": s.id("lbl"), "protocol": orDefault(body["protocol"], "tcp"), "port": body["port"],
			"target_group_id": body["target_group_id"], "created_at": now(),
		})
		return s.lbChanged(ip, o)
	case rest[0] == "listeners" && len(rest) == 2:
		listeners := lbList(o, "listeners")
		for i, x := range listeners {
			l := x.(map[string]any)
			if l["id"] != rest[1] {
				continue
			}
			switch method {
			case http.MethodPatch:
				if !hasGroup(o, body["target_group_id"]) {
					return 0, nil, fail(404, "NotFound", "Target group not found")
				}
				l["target_group_id"] = body["target_group_id"]
				return s.lbChanged(ip, o)
			case http.MethodDelete:
				o["listeners"] = append(append([]any{}, listeners[:i]...), listeners[i+1:]...)
				return s.lbChanged(ip, o)
			}
		}
		return 0, nil, notFound("Listener")
	}
	return 0, nil, notFound("Route")
}

func hasGroup(o map[string]any, id any) bool {
	for _, g := range lbList(o, "target_groups") {
		if g.(map[string]any)["id"] == id {
			return true
		}
	}
	return false
}

func orDefault(v any, def string) any {
	if v == nil || v == "" {
		return def
	}
	return v
}

func healthCheck(v any) map[string]any {
	h := map[string]any{
		"protocol": "tcp", "port": nil, "path": "/", "matcher": "200-399",
		"interval_seconds": 10, "timeout_seconds": 5, "healthy_threshold": 3, "unhealthy_threshold": 3,
	}
	if in, ok := v.(map[string]any); ok {
		for k, x := range in {
			if x != nil {
				h[k] = x
			}
		}
	}
	return h
}

// -- DNS --------------------------------------------------------------------------------------

// canonicalValue mimics the API's presentation form: names fully qualified, TXT quoted.
func canonicalValue(rtype, v, domain string) string {
	qualify := func(name string) string {
		if strings.HasSuffix(name, ".") {
			return name
		}
		return name + "."
	}
	switch rtype {
	case "CNAME", "NS":
		return qualify(v)
	case "MX":
		f := strings.Fields(v)
		if len(f) == 2 {
			return f[0] + " " + qualify(f[1])
		}
	case "TXT":
		if !strings.HasPrefix(v, `"`) {
			return `"` + v + `"`
		}
	}
	return v
}

func (s *Server) dns(method, project string, parts []string, body map[string]any) (int, any, *apiErr) {
	pp := "/projects/" + project
	cp := pp + "/dns/zones"
	if len(parts) == 0 {
		switch method {
		case http.MethodGet:
			var items []any
			for _, p := range s.sortedPaths(cp) {
				if o, err := s.get(p); err == nil {
					items = append(items, o)
				}
			}
			return 200, listPage(items), nil
		case http.MethodPost:
			domain := strings.TrimSuffix(strings.ToLower(str(body["domain"])), ".")
			if domain == "" {
				return 0, nil, fail(422, "InvalidRequest", "domain: field required")
			}
			name := str(body["name"])
			if name == "" {
				name = domain
			}
			for _, p := range s.sortedPaths(cp) {
				if s.objs[p]["domain"] == domain {
					return 0, nil, conflict("The project already has a zone for " + domain)
				}
			}
			if s.nameTaken(cp, name) {
				return 0, nil, conflict(fmt.Sprintf("A zone named %q already exists", name))
			}
			id := s.id("zone")
			o := meta(id, project, name, "dns", body)
			o["domain"], o["status"], o["served"], o["serial"], o["delegation_ok"] = domain, "pending_verification", false, nil, nil
			o["nameservers"] = []any{"ns1.tebibhost.com", "ns2.tebibhost.com"}
			o["record_sets"] = 0
			o["verification"] = map[string]any{
				"state": "pending", "method": nil, "txt_name": "_tebib-verify." + domain,
				"txt_value": "tebib-verify=" + id, "message": "", "last_checked_at": nil, "next_check_at": nil,
			}
			// Like the API: "creating" until the domain is verified (VerifyZone).
			s.put(cp+"/"+id, o)
			return 202, o, nil
		}
		return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
	}
	zp := cp + "/" + parts[0]
	zone, ok := s.objs[zp]
	if !ok {
		return 0, nil, notFound("Zone")
	}
	if len(parts) == 1 {
		switch method {
		case http.MethodPatch:
			if zone["state"] == "creating" {
				// A pending zone takes description and tags, and stays pending.
				for _, k := range []string{"description", "tags"} {
					if v, ok := body[k]; ok && v != nil {
						zone[k] = v
					}
				}
				return 200, zone, nil
			}
		case http.MethodDelete:
			for p := range s.objs {
				if strings.HasPrefix(p, zp+"/records/") {
					delete(s.objs, p)
				}
			}
		}
		return s.item(method, zp, body)
	}
	if parts[1] != "records" {
		return 0, nil, notFound("Route")
	}
	if zone["state"] == "deleting" {
		return 0, nil, conflict("The zone is being deleted")
	}
	domain := str(zone["domain"])
	rp := zp + "/records"
	if len(parts) == 2 {
		switch method {
		case http.MethodGet:
			var items []any
			for _, p := range s.sortedPaths(rp) {
				items = append(items, s.objs[p])
			}
			return 200, listPage(items), nil
		case http.MethodPost:
			name := strings.ToLower(strings.TrimSpace(str(orDefault(body["name"], "@"))))
			name = strings.TrimSuffix(strings.TrimSuffix(name, "."), "."+domain)
			if name == domain || name == "" {
				name = "@"
			}
			rtype := str(body["type"])
			for _, p := range s.sortedPaths(rp) {
				if s.objs[p]["name"] == name && s.objs[p]["type"] == rtype {
					return 0, nil, conflict(fmt.Sprintf("A %s record set named %s already exists", rtype, name))
				}
			}
			raw, _ := body["values"].([]any)
			var values []any
			for _, v := range raw {
				values = append(values, canonicalValue(rtype, str(v), domain))
			}
			fqdn := domain + "."
			if name != "@" {
				fqdn = name + "." + domain + "."
			}
			id := s.id("rec")
			ttl := body["ttl"]
			if ttl == nil {
				ttl = 3600
			}
			o := map[string]any{
				"id": id, "zone_id": zone["id"], "name": name, "fqdn": fqdn, "type": rtype, "ttl": ttl,
				"values": values, "description": str(body["description"]), "created_at": now(), "updated_at": now(),
			}
			s.put(rp+"/"+id, o)
			return 202, o, nil
		}
		return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
	}
	p := rp + "/" + parts[2]
	rec, ok := s.objs[p]
	if !ok {
		return 0, nil, notFound("Record")
	}
	switch method {
	case http.MethodGet:
		return 200, rec, nil
	case http.MethodPatch:
		if v := body["ttl"]; v != nil {
			rec["ttl"] = v
		}
		if v := body["description"]; v != nil {
			rec["description"] = v
		}
		if raw, ok := body["values"].([]any); ok {
			var values []any
			for _, v := range raw {
				values = append(values, canonicalValue(str(rec["type"]), str(v), domain))
			}
			rec["values"] = values
		}
		return 202, rec, nil
	case http.MethodDelete:
		delete(s.objs, p)
		return 202, rec, nil
	}
	return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
}
