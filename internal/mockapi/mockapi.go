// Package mockapi is an in-memory fake of the TebibHost API for the provider's unit tests.
//
// It keeps resources as JSON objects and plays the asynchronous model of the real API
// (ADR 0032): creates, changes and deletes answer 202 with a transitional state
// (creating, updating, deleting), and the resource settles after a few GETs. It checks the
// bearer token and the rules the provider relies on (a VM must be stopped to resize; a
// volume attached elsewhere can't be attached; public IPs on VMs need a NAT gateway).
// It is not a full model of the control plane.
package mockapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// Token is the bearer token the fake accepts.
	Token = "TBKTEST.secret"
	// Org and Project exist from the start, with a default VPC, subnet and security group.
	Org     = "o-test"
	Project = "p-test"
)

// Server is the fake API.
type Server struct {
	*httptest.Server
	// Settle is how many GETs a transitional resource needs to settle (default 1).
	Settle int

	mu      sync.Mutex
	objs    map[string]map[string]any // item path (no /api/v1) -> object
	order   map[string]int            // item path -> creation order
	pending map[string]*transition
	seq     int
	calls   []string
	// FailCreate makes the next create of a collection settle in state "error".
	failCreate map[string]string
}

type transition struct {
	left  int
	gone  bool
	apply map[string]any
}

// New starts a fake API. Close it when done.
func New() *Server {
	s := &Server{
		Settle:     1,
		objs:       map[string]map[string]any{},
		order:      map[string]int{},
		pending:    map[string]*transition{},
		failCreate: map[string]string{},
	}
	s.seedProject(Org, Project, "test")
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Calls returns "METHOD /path" for every call so far.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// Object returns a copy of a stored object (nil if absent). Path is like
// /projects/p-test/vms/vm-1.
func (s *Server) Object(path string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objs[path]
	if !ok {
		return nil
	}
	return clone(o)
}

// Mutate changes a stored object out of band (drift), e.g. a change in the dashboard.
func (s *Server) Mutate(path string, fn func(map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objs[path]; ok {
		fn(o)
	}
}

// Remove deletes a stored object out of band, with the side effects of a real delete.
func (s *Server) Remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objs[path]; ok && strings.HasPrefix(path, "/projects/") {
		s.onGone(path, o)
	}
	delete(s.objs, path)
	delete(s.pending, path)
}

// Find returns the path of the first object in a collection whose field equals value.
func (s *Server) Find(collectionPath, field string, value any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.sortedPaths(collectionPath) {
		if fmt.Sprint(s.objs[p][field]) == fmt.Sprint(value) {
			return p
		}
	}
	return ""
}

// Count is the number of objects in a collection, e.g. /orgs/o-test/roles.
func (s *Server) Count(collectionPath string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sortedPaths(collectionPath))
}

// FailNextCreate makes the next resource created in collection (e.g. "vms") end in error.
func (s *Server) FailNextCreate(collection, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCreate[collection] = message
}

func clone(o map[string]any) map[string]any {
	b, _ := json.Marshal(o)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Server) id(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%d", prefix, s.seq)
}

func (s *Server) put(path string, o map[string]any) {
	if _, ok := s.objs[path]; !ok {
		s.seq++
		s.order[path] = s.seq
	}
	s.objs[path] = o
}

func (s *Server) sortedPaths(collectionPath string) []string {
	prefix := collectionPath + "/"
	var out []string
	for p := range s.objs {
		if strings.HasPrefix(p, prefix) && !strings.Contains(p[len(prefix):], "/") {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return s.order[out[i]] < s.order[out[j]] })
	return out
}

func (s *Server) settleLater(path string, gone bool, apply map[string]any) {
	s.pending[path] = &transition{left: s.Settle, gone: gone, apply: apply}
}

func meta(id, project, name, kind string, body map[string]any) map[string]any {
	o := map[string]any{
		"id": id, "trn": fmt.Sprintf("trn:tebib:%s:lab-neo-1:%s:%s", kind, project, id),
		"name": name, "description": str(body["description"]), "tags": tagsOf(body["tags"]),
		"project_id": project, "state": "creating", "status_message": "",
		"created_at": now(), "updated_at": now(),
	}
	return o
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func tagsOf(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func (s *Server) seedProject(org, project, name string) {
	pp := "/projects/" + project
	s.put(pp, map[string]any{
		"id": project, "trn": "trn:tebib:org::" + org + ":project/" + project, "name": name,
		"description": "", "tags": map[string]any{}, "organization_id": org,
		"state": "active", "status_message": "", "created_at": now(), "updated_at": now(),
	})
	vpc := s.id("vpc")
	v := meta(vpc, project, "default", "network", nil)
	v["state"], v["cidr"], v["ipv6_cidr"], v["dns_server"] = "active", "10.0.0.0/16", "2001:db8:0:1::/80", "100.67.53.53"
	v["is_default"], v["has_nat_gateway"] = true, false
	s.put(pp+"/vpcs/"+vpc, v)
	sub := s.id("subnet")
	sn := meta(sub, project, "default", "network", nil)
	sn["state"], sn["vpc_id"], sn["cidr"], sn["gateway"] = "active", vpc, "10.0.0.0/24", "10.0.0.1"
	sn["ipv6_cidr"], sn["ipv6_gateway"], sn["available_ipv4"], sn["is_default"] = "2001:db8:0:1::/96", "2001:db8:0:1::1", 250, true
	s.put(pp+"/subnets/"+sub, sn)
	sg := s.id("sg")
	g := meta(sg, project, "default", "network", nil)
	g["state"], g["vpc_id"], g["is_default"], g["allow_same_group"], g["rules"] = "active", vpc, true, true, []any{}
	s.put(pp+"/security-groups/"+sg, g)
}

// -- HTTP -----------------------------------------------------------------------------------

type apiErr struct {
	status int
	code   string
	msg    string
}

func fail(status int, code, msg string) *apiErr { return &apiErr{status, code, msg} }

func notFound(what string) *apiErr { return fail(404, "NotFound", what+" not found") }

func conflict(msg string) *apiErr { return fail(409, "Conflict", msg) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("X-Request-ID", fmt.Sprintf("req-%d", len(s.calls)))
	if r.Header.Get("Authorization") != "Bearer "+Token {
		writeErr(w, fail(401, "Unauthenticated", "Missing or invalid credentials"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if path == r.URL.Path {
		writeErr(w, notFound("Route"))
		return
	}
	var body map[string]any
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		if len(data) > 0 {
			if err := json.Unmarshal(data, &body); err != nil {
				writeErr(w, fail(400, "InvalidRequest", "body is not JSON"))
				return
			}
		}
	}
	if body == nil {
		body = map[string]any{}
	}
	status, out, e := s.route(r.Method, path, r.URL.Query(), body)
	if e != nil {
		writeErr(w, e)
		return
	}
	if out == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

func writeErr(w http.ResponseWriter, e *apiErr) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": e.code, "message": e.msg, "request_id": w.Header().Get("X-Request-ID"),
	}})
}

func (s *Server) route(method, path string, q map[string][]string, body map[string]any) (int, any, *apiErr) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/prices" && method == http.MethodGet:
		return 200, prices(), nil
	case parts[0] == "orgs" && len(parts) >= 3:
		if parts[1] != Org {
			return 0, nil, fail(403, "AccessDenied", "Not a member of "+parts[1])
		}
		return s.org(method, parts[2:], q, body)
	case parts[0] == "projects" && len(parts) >= 2:
		if _, ok := s.objs["/projects/"+parts[1]]; !ok {
			return 0, nil, notFound("Project")
		}
		if len(parts) == 2 {
			return s.item(method, "/projects/"+parts[1], body)
		}
		return s.project(method, parts[1], parts[2:], q, body)
	}
	return 0, nil, notFound("Route")
}

// get returns an object after counting one poll against its pending transition.
func (s *Server) get(path string) (map[string]any, *apiErr) {
	o, ok := s.objs[path]
	if !ok {
		return nil, notFound("Resource")
	}
	if t := s.pending[path]; t != nil {
		t.left--
		if t.left <= 0 {
			delete(s.pending, path)
			if t.gone {
				s.onGone(path, o)
				delete(s.objs, path)
				return nil, notFound("Resource")
			}
			for k, v := range t.apply {
				o[k] = v
			}
			if o["state"] != "error" {
				o["state"] = "active"
			}
			o["updated_at"] = now()
			s.onSettled(path, o)
		}
	}
	return o, nil
}

// item handles GET/PATCH/DELETE on a resource with a lifecycle.
func (s *Server) item(method, path string, body map[string]any) (int, any, *apiErr) {
	o, ok := s.objs[path]
	if !ok {
		return 0, nil, notFound("Resource")
	}
	switch method {
	case http.MethodGet:
		got, e := s.get(path)
		if e != nil {
			return 0, nil, e
		}
		return 200, got, nil
	case http.MethodPatch:
		if o["state"] == "deleting" {
			return 0, nil, conflict("It is being deleted")
		}
		for _, k := range []string{"description", "tags", "delete_on_termination"} {
			if v, ok := body[k]; ok && v != nil {
				o[k] = v
			}
		}
		o["state"] = "updating"
		s.settleLater(path, false, nil)
		return 200, o, nil
	case http.MethodDelete:
		if e := s.canDelete(path, o); e != nil {
			return 0, nil, e
		}
		o["state"] = "deleting"
		s.settleLater(path, true, nil)
		return 202, o, nil
	}
	return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
}

func (s *Server) canDelete(path string, o map[string]any) *apiErr {
	switch {
	case strings.Contains(path, "/volumes/"):
		if o["vm_id"] != nil && o["boot"] != true {
			return conflict("The volume is attached to " + str(o["vm_id"]) + "; detach it first")
		}
	case strings.Contains(path, "/subnets/"):
		project := strings.Split(path, "/")[2]
		for _, p := range s.sortedPaths("/projects/" + project + "/vms") {
			if s.objs[p]["subnet_id"] == o["id"] {
				return conflict("A VM still uses the subnet")
			}
		}
	}
	return nil
}

// onSettled applies side effects when a resource becomes active.
func (s *Server) onSettled(path string, o map[string]any) {
	if strings.Contains(path, "/vms/") && o["desired_power_state"] == "running" && o["state"] == "active" {
		o["power_state"] = "running"
		o["addresses"] = []any{
			map[string]any{"address": "10.0.0.10", "family": "ipv4", "primary": true},
			map[string]any{"address": "2001:db8:0:1::10", "family": "ipv6", "primary": false},
		}
		o["guest_agent"] = true
		vols := []any{}
		project := strings.Split(path, "/")[2]
		for _, p := range s.sortedPaths("/projects/" + project + "/volumes") {
			if s.objs[p]["vm_id"] == o["id"] {
				vols = append(vols, map[string]any{"volume_id": s.objs[p]["id"], "state": "attached"})
			}
		}
		o["volumes"] = vols
	}
	if strings.Contains(path, "/vms/") && o["desired_power_state"] == "stopped" {
		o["power_state"] = "stopped"
	}
}

// onGone applies side effects when a deleted resource goes away.
func (s *Server) onGone(path string, o map[string]any) {
	project := strings.Split(path, "/")[2]
	pp := "/projects/" + project
	switch {
	case strings.Contains(path, "/vms/"):
		for _, p := range s.sortedPaths(pp + "/volumes") {
			v := s.objs[p]
			if v["vm_id"] == o["id"] {
				if v["boot"] == true || v["delete_on_termination"] == true {
					delete(s.objs, p)
				} else {
					v["vm_id"] = nil
				}
			}
		}
		for _, p := range s.sortedPaths(pp + "/public-ips") {
			ip := s.objs[p]
			if ip["vm_id"] == o["id"] {
				ip["vm_id"], ip["nat_gateway_id"], ip["association"] = nil, nil, "none"
			}
		}
	case strings.Contains(path, "/nat-gateways/"):
		if vpc := s.objs[pp+"/vpcs/"+str(o["vpc_id"])]; vpc != nil {
			vpc["has_nat_gateway"] = false
		}
	case path == pp:
		for p := range s.objs {
			if strings.HasPrefix(p, pp+"/") {
				delete(s.objs, p)
			}
		}
	}
}

func listPage(items []any) map[string]any {
	if items == nil {
		items = []any{}
	}
	return map[string]any{"items": items, "next_cursor": nil}
}

// project handles /projects/{p}/{collection}...
func (s *Server) project(method, project string, parts []string, q map[string][]string, body map[string]any) (int, any, *apiErr) {
	pp := "/projects/" + project
	coll := parts[0]
	cp := pp + "/" + coll
	switch coll {
	case "images":
		return 200, images(), nil
	case "instance-types":
		return 200, instanceTypes(), nil
	}
	if len(parts) == 1 {
		switch method {
		case http.MethodGet:
			var items []any
			for _, p := range s.sortedPaths(cp) {
				o, e := s.get(p)
				if e != nil {
					continue
				}
				if !matchQuery(o, q) {
					continue
				}
				items = append(items, o)
			}
			// Exercise cursor-following: answer in pages of 2 when the client asks for few.
			return 200, listPage(items), nil
		case http.MethodPost:
			return s.create(project, coll, body)
		}
		return 0, nil, fail(405, "InvalidRequest", "Method not allowed")
	}
	ip := cp + "/" + parts[1]
	if len(parts) == 2 {
		return s.item(method, ip, body)
	}
	o, ok := s.objs[ip]
	if !ok {
		return 0, nil, notFound("Resource")
	}
	return s.action(method, project, coll, ip, o, parts[2:], body)
}

func matchQuery(o map[string]any, q map[string][]string) bool {
	for _, k := range []string{"vpc_id", "vm_id"} {
		if v := q[k]; len(v) > 0 && v[0] != "" && str(o[k]) != v[0] {
			return false
		}
	}
	return true
}

var prefixes = map[string]string{
	"vpcs": "vpc", "subnets": "subnet", "security-groups": "sg", "nat-gateways": "nat",
	"public-ips": "eip", "ssh-keys": "key", "vms": "vm", "volumes": "vol", "snapshots": "snap",
}

var services = map[string]string{
	"vpcs": "network", "subnets": "network", "security-groups": "network", "nat-gateways": "network",
	"public-ips": "network", "ssh-keys": "compute", "vms": "compute", "volumes": "compute", "snapshots": "compute",
}

func (s *Server) nameTaken(cp, name string) bool {
	for _, p := range s.sortedPaths(cp) {
		if s.objs[p]["name"] == name {
			return true
		}
	}
	return false
}

func (s *Server) create(project, coll string, body map[string]any) (int, any, *apiErr) {
	prefix, ok := prefixes[coll]
	if !ok {
		return 0, nil, notFound("Route")
	}
	pp := "/projects/" + project
	cp := pp + "/" + coll
	name := str(body["name"])
	if name == "" {
		return 0, nil, fail(422, "InvalidRequest", "name: field required")
	}
	if s.nameTaken(cp, name) {
		return 0, nil, conflict(fmt.Sprintf("A resource named %q already exists", name))
	}
	id := s.id(prefix)
	o := meta(id, project, name, services[coll], body)
	var apply map[string]any
	switch coll {
	case "vpcs":
		o["cidr"], o["ipv6_cidr"], o["dns_server"] = body["cidr"], "2001:db8:0:9::/80", "100.67.53.53"
		o["is_default"], o["has_nat_gateway"] = false, false
	case "subnets":
		if _, ok := s.objs[pp+"/vpcs/"+str(body["vpc_id"])]; !ok {
			return 0, nil, fail(400, "InvalidRequest", "vpc_id: VPC not found")
		}
		o["vpc_id"], o["cidr"], o["gateway"] = body["vpc_id"], body["cidr"], "10.1.1.1"
		o["ipv6_cidr"], o["ipv6_gateway"], o["available_ipv4"], o["is_default"] = "2001:db8:0:9::/96", "2001:db8:0:9::1", 250, false
	case "security-groups":
		o["vpc_id"], o["is_default"], o["allow_same_group"] = body["vpc_id"], false, false
		o["rules"] = []any{map[string]any{
			"id": s.id("sgr"), "direction": "egress", "protocol": "all", "port_from": nil, "port_to": nil,
			"remote_cidr": "0.0.0.0/0", "remote_group_id": nil, "ip_version": "ipv4",
			"description": "default egress", "created_at": now(),
		}}
	case "nat-gateways":
		subnet := s.objs[pp+"/subnets/"+str(body["subnet_id"])]
		if subnet == nil {
			return 0, nil, fail(400, "InvalidRequest", "subnet_id: subnet not found")
		}
		o["subnet_id"], o["vpc_id"], o["lan_ip"] = body["subnet_id"], subnet["vpc_id"], "10.1.1.2"
		o["public_ip_id"], o["public_address"] = nil, nil
		if ipID := str(body["public_ip_id"]); ipID != "" {
			ipObj := s.objs[pp+"/public-ips/"+ipID]
			if ipObj == nil {
				return 0, nil, fail(400, "InvalidRequest", "public_ip_id: not found")
			}
			ipObj["association"], ipObj["nat_gateway_id"] = "nat-gateway", id
			o["public_ip_id"], o["public_address"] = ipID, ipObj["address"]
		}
		if vpc := s.objs[pp+"/vpcs/"+str(subnet["vpc_id"])]; vpc != nil {
			vpc["has_nat_gateway"] = true
		}
	case "public-ips":
		o["address"] = fmt.Sprintf("203.0.113.%d", s.seq%250+1)
		o["association"], o["vm_id"], o["nat_gateway_id"] = "none", nil, nil
	case "ssh-keys":
		key := strings.Join(strings.Fields(str(body["public_key"])), " ")
		if !strings.HasPrefix(key, "ssh-") && !strings.HasPrefix(key, "ecdsa-") {
			return 0, nil, fail(400, "InvalidRequest", "public_key: not an OpenSSH public key")
		}
		o["public_key"], o["key_type"] = key, strings.Fields(key)[0]
		o["fingerprint"] = "SHA256:" + fmt.Sprintf("%x", len(key))
		o["state"] = "active" // SSH keys are ready at once
	case "vms":
		if e := s.newVM(pp, id, o, body); e != nil {
			return 0, nil, e
		}
	case "volumes":
		o["size_gib"], o["delete_on_termination"] = body["size_gib"], body["delete_on_termination"] == true
		o["vm_id"], o["boot"], o["source_image"] = nil, false, nil
	case "snapshots":
		vm := s.objs[pp+"/vms/"+str(body["vm_id"])]
		if vm == nil {
			return 0, nil, fail(400, "InvalidRequest", "vm_id: VM not found")
		}
		var vols []any
		var size float64
		for _, p := range s.sortedPaths(pp + "/volumes") {
			if s.objs[p]["vm_id"] == vm["id"] {
				vols = append(vols, s.objs[p]["id"])
				size += toFloat(s.objs[p]["size_gib"])
			}
		}
		o["vm_id"], o["volume_ids"], o["size_gib"], o["consistency"], o["taken_at"] = vm["id"], vols, size, nil, nil
		apply = map[string]any{"consistency": "application", "taken_at": now()}
	}
	if msg, ok := s.failCreate[coll]; ok {
		delete(s.failCreate, coll)
		if apply == nil {
			apply = map[string]any{}
		}
		apply["state"], apply["status_message"] = "error", msg
	}
	s.put(cp+"/"+id, o)
	if o["state"] == "creating" {
		s.settleLater(cp+"/"+id, false, apply)
	}
	return 202, o, nil
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

func (s *Server) newVM(pp, id string, o, body map[string]any) *apiErr {
	itype := findType(str(body["instance_type"]))
	if itype == nil {
		return fail(400, "InvalidRequest", "instance_type: unknown")
	}
	image := findImage(str(body["image"]))
	if image == nil {
		return fail(400, "InvalidRequest", "image: unknown")
	}
	subnetID := str(body["subnet_id"])
	if subnetID == "" {
		subnetID = str(s.objs[s.findDefault(pp+"/subnets")]["id"])
	}
	subnet := s.objs[pp+"/subnets/"+subnetID]
	if subnet == nil {
		return fail(400, "InvalidRequest", "subnet_id: subnet not found")
	}
	groups, _ := body["security_group_ids"].([]any)
	if len(groups) == 0 {
		for _, p := range s.sortedPaths(pp + "/security-groups") {
			if s.objs[p]["vpc_id"] == subnet["vpc_id"] && s.objs[p]["is_default"] == true {
				groups = []any{s.objs[p]["id"]}
			}
		}
	}
	keys, _ := body["ssh_key_ids"].([]any)
	if keys == nil {
		keys = []any{}
	}
	for _, k := range keys {
		if _, ok := s.objs[pp+"/ssh-keys/"+str(k)]; !ok {
			return fail(400, "InvalidRequest", "ssh_key_ids: key "+str(k)+" not found")
		}
	}
	power := str(body["power_state"])
	if power == "" {
		power = "running"
	}
	hostname := str(body["hostname"])
	if hostname == "" {
		hostname = str(body["name"])
	}
	disk := toFloat(body["root_disk_gib"])
	if disk == 0 {
		disk = toFloat(image["min_disk_gib"])
	}
	boot := s.id("vol")
	bv := meta(boot, str(o["project_id"]), str(body["name"])+"-boot", "compute", nil)
	bv["state"], bv["size_gib"], bv["vm_id"], bv["boot"] = "active", disk, id, true
	bv["source_image"], bv["delete_on_termination"] = image["name"], true
	s.put(pp+"/volumes/"+boot, bv)
	o["image"], o["instance_type"], o["vcpus"], o["memory_mib"] = image["name"], itype["name"], itype["vcpus"], itype["memory_mib"]
	o["hostname"], o["subnet_id"], o["security_group_ids"], o["ssh_key_ids"] = hostname, subnetID, groups, keys
	o["power_state"], o["desired_power_state"], o["restart_required"] = "stopped", power, false
	o["boot_volume_id"], o["addresses"], o["volumes"], o["guest_agent"] = boot, []any{}, []any{}, false
	o["guest_os"], o["last_action"], o["stop_reason"], o["held_reason"] = nil, nil, nil, nil
	o["user_data_seen"] = body["user_data"] // for tests; the real API never returns it
	return nil
}

func (s *Server) findDefault(cp string) string {
	for _, p := range s.sortedPaths(cp) {
		if s.objs[p]["is_default"] == true {
			return p
		}
	}
	return ""
}

func (s *Server) action(method, project, coll, ip string, o map[string]any, rest []string, body map[string]any) (int, any, *apiErr) {
	pp := "/projects/" + project
	act := rest[0]
	key := coll + "/" + act
	switch {
	case key == "vms/start" && method == http.MethodPost:
		o["desired_power_state"] = "running"
		o["state"] = "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	case key == "vms/stop" && method == http.MethodPost:
		o["desired_power_state"] = "stopped"
		o["state"] = "updating"
		s.settleLater(ip, false, map[string]any{"power_state": "stopped", "addresses": []any{}})
		return 202, o, nil
	case key == "vms/resize" && method == http.MethodPost:
		if o["power_state"] != "stopped" {
			return 0, nil, conflict("Stop the VM and wait until it is stopped before resizing it")
		}
		t := findType(str(body["instance_type"]))
		if t == nil {
			return 0, nil, fail(400, "InvalidRequest", "instance_type: unknown")
		}
		o["instance_type"], o["vcpus"], o["memory_mib"] = t["name"], t["vcpus"], t["memory_mib"]
		o["state"] = "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	case key == "vms/security-groups" && method == http.MethodPut:
		o["security_group_ids"] = body["security_group_ids"]
		o["state"] = "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	case key == "volumes/attach" && method == http.MethodPost:
		vmID := str(body["vm_id"])
		if s.objs[pp+"/vms/"+vmID] == nil {
			return 0, nil, fail(400, "InvalidRequest", "vm_id: VM "+vmID+" not found")
		}
		if o["vm_id"] == vmID {
			return 202, o, nil
		}
		if o["vm_id"] != nil {
			return 0, nil, conflict(fmt.Sprintf("The volume is attached to %s; detach it first", o["vm_id"]))
		}
		o["vm_id"], o["state"] = vmID, "updating"
		s.settleLater(ip, false, nil)
		vm := s.objs[pp+"/vms/"+vmID]
		vols, _ := vm["volumes"].([]any)
		vm["volumes"] = append(vols, map[string]any{"volume_id": o["id"], "state": "attached"})
		return 202, o, nil
	case key == "volumes/detach" && method == http.MethodPost:
		if o["boot"] == true {
			return 0, nil, conflict("The boot volume can't be detached; delete the VM to free it")
		}
		if vm := s.objs[pp+"/vms/"+str(o["vm_id"])]; vm != nil {
			var keep []any
			vols, _ := vm["volumes"].([]any)
			for _, v := range vols {
				if m, ok := v.(map[string]any); ok && m["volume_id"] != o["id"] {
					keep = append(keep, v)
				}
			}
			vm["volumes"] = keep
		}
		o["vm_id"], o["state"] = nil, "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	case key == "volumes/resize" && method == http.MethodPost:
		size := toFloat(body["size_gib"])
		if size < toFloat(o["size_gib"]) {
			return 0, nil, fail(400, "InvalidRequest", "size_gib: volumes can only grow")
		}
		o["size_gib"], o["state"] = size, "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	case key == "public-ips/associate" && method == http.MethodPost:
		if vmID := str(body["vm_id"]); vmID != "" {
			vm := s.objs[pp+"/vms/"+vmID]
			if vm == nil {
				return 0, nil, fail(400, "InvalidRequest", "vm_id: VM not found")
			}
			subnet := s.objs[pp+"/subnets/"+str(vm["subnet_id"])]
			nat := ""
			for _, p := range s.sortedPaths(pp + "/nat-gateways") {
				if s.objs[p]["vpc_id"] == subnet["vpc_id"] {
					nat = str(s.objs[p]["id"])
				}
			}
			if nat == "" {
				return 0, nil, conflict(fmt.Sprintf("VPC %s has no NAT gateway: public IPs reach VMs through it. Create one first", subnet["vpc_id"]))
			}
			o["association"], o["vm_id"], o["nat_gateway_id"] = "vm", vmID, nat
		} else {
			natID := str(body["nat_gateway_id"])
			nat := s.objs[pp+"/nat-gateways/"+natID]
			if nat == nil {
				return 0, nil, fail(400, "InvalidRequest", "nat_gateway_id: not found")
			}
			o["association"], o["vm_id"], o["nat_gateway_id"] = "nat-gateway", nil, natID
			nat["public_ip_id"], nat["public_address"] = o["id"], o["address"]
		}
		o["state"] = "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	case key == "public-ips/disassociate" && method == http.MethodPost:
		if nat := s.objs[pp+"/nat-gateways/"+str(o["nat_gateway_id"])]; nat != nil && o["association"] == "nat-gateway" {
			nat["public_ip_id"], nat["public_address"] = nil, nil
		}
		o["association"], o["vm_id"], o["nat_gateway_id"], o["state"] = "none", nil, nil, "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	case key == "security-groups/rules" && method == http.MethodPost && len(rest) == 1:
		return s.addRule(ip, o, body)
	case key == "security-groups/rules" && method == http.MethodDelete && len(rest) == 2:
		rules, _ := o["rules"].([]any)
		var keep []any
		found := false
		for _, r := range rules {
			if r.(map[string]any)["id"] == rest[1] {
				found = true
				continue
			}
			keep = append(keep, r)
		}
		if !found {
			return 0, nil, notFound("Rule")
		}
		if keep == nil {
			keep = []any{}
		}
		o["rules"], o["state"] = keep, "updating"
		s.settleLater(ip, false, nil)
		return 202, o, nil
	}
	return 0, nil, notFound("Route")
}

func (s *Server) addRule(ip string, o map[string]any, body map[string]any) (int, any, *apiErr) {
	cidr := body["remote_cidr"]
	version := body["ip_version"]
	if cidr != nil {
		c := str(cidr)
		if c == "10.0.0.1/24" { // the API normalizes ranges (strict=False)
			c = "10.0.0.0/24"
		}
		cidr = c
		if version == nil {
			version = "ipv4"
			if strings.Contains(c, ":") {
				version = "ipv6"
			}
		}
	}
	portTo := body["port_to"]
	if body["port_from"] != nil && portTo == nil {
		portTo = body["port_from"]
	}
	rules, _ := o["rules"].([]any)
	for _, r := range rules {
		m := r.(map[string]any)
		if m["direction"] == body["direction"] && m["protocol"] == body["protocol"] &&
			str(m["port_from"]) == str(body["port_from"]) && str(m["port_to"]) == str(portTo) &&
			str(m["remote_cidr"]) == str(cidr) && str(m["remote_group_id"]) == str(body["remote_group_id"]) {
			return 0, nil, conflict("The same rule already exists")
		}
	}
	o["rules"] = append(rules, map[string]any{
		"id": s.id("sgr"), "direction": body["direction"], "protocol": body["protocol"],
		"port_from": body["port_from"], "port_to": portTo, "remote_cidr": cidr,
		"remote_group_id": body["remote_group_id"], "ip_version": version,
		"description": str(body["description"]), "created_at": now(),
	})
	o["state"] = "updating"
	s.settleLater(ip, false, nil)
	return 202, o, nil
}

// -- catalogue ------------------------------------------------------------------------------

func images() []any {
	return []any{
		map[string]any{"name": "ubuntu-24.04", "display_name": "Ubuntu 24.04 LTS", "os_family": "ubuntu",
			"default_user": "ubuntu", "min_disk_gib": 10, "min_memory_mib": 512, "min_vcpus": 1},
		map[string]any{"name": "debian-13", "display_name": "Debian 13", "os_family": "debian",
			"default_user": "debian", "min_disk_gib": 10, "min_memory_mib": 512, "min_vcpus": 1},
	}
}

func findImage(name string) map[string]any {
	for _, i := range images() {
		if m := i.(map[string]any); m["name"] == name {
			return m
		}
	}
	return nil
}

func instanceTypes() []any {
	return []any{
		map[string]any{"name": "b1.small", "description": "2 vCPU, 2 GiB", "family": "b1", "vcpus": 2, "memory_mib": 2048},
		map[string]any{"name": "b1.medium", "description": "2 vCPU, 4 GiB", "family": "b1", "vcpus": 2, "memory_mib": 4096},
		map[string]any{"name": "g1.large", "description": "4 vCPU, 8 GiB", "family": "g1", "vcpus": 4, "memory_mib": 8192},
	}
}

func findType(name string) map[string]any {
	for _, i := range instanceTypes() {
		if m := i.(map[string]any); m["name"] == name {
			return m
		}
	}
	return nil
}

func prices() map[string]any {
	price := func(meter, unit, usd string, micro int, monthly any) map[string]any {
		return map[string]any{"meter": meter, "unit": unit, "unit_price_usd": usd, "unit_price_micro": micro,
			"monthly_usd": monthly, "note": "", "effective_from": "2026-01-01T00:00:00Z"}
	}
	return map[string]any{
		"at": now(), "currency": "USD", "region": "lab-neo-1", "hours_per_month": 730,
		"prices": []any{
			price("vm.b1.small", "hour", "0.0100", 10000, "7.30"),
			price("volume.gib", "gib-hour", "0.0001", 100, nil),
		},
		"scheduled":         []any{},
		"egress_allowances": []any{map[string]any{"instance_type": "b1.small", "gb_per_month": 100, "effective_from": "2026-01-01T00:00:00Z"}},
	}
}
