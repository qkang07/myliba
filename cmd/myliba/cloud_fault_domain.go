package main

import (
	"os"
	"sort"
	"strings"
)

// cloudFaultDomain derives a stable identifier for the physical storage behind a
// node. Two gateways that point at the same backing store (the same WebDAV root
// or the same S3 bucket/prefix) resolve to the same domain, so they never count
// as two independent saves. An explicit MYLIBA_FAULT_DOMAIN lets an operator
// declare that distinct local directories share one physical disk. A local disk
// node defaults to its own node identity (an independent disk); nodes that report
// no domain at all are treated as unknown and excluded from independent counts.
func cloudFaultDomain(disk *cloudDisk, id string) string {
	if override := strings.TrimSpace(os.Getenv("MYLIBA_FAULT_DOMAIN")); override != "" {
		return override
	}
	if disk != nil && disk.backend != nil {
		switch backend := disk.backend.(type) {
		case *s3Backend:
			return "s3:" + cloudHash([]byte(backend.endpoint.Host+"/"+backend.bucket+"/"+backend.prefix))
		case *openListBackend:
			return "openlist:" + cloudHash([]byte(backend.root.String()))
		case *webDAVBackend:
			return "webdav:" + cloudHash([]byte(backend.root.String()))
		}
	}
	return "disk:" + id
}

func validCloudFaultDomain(domain string) bool {
	if len(domain) > 160 {
		return false
	}
	for _, r := range domain {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// nodeFaultDomains maps every known node ID (self plus discovered peers) to its
// reported fault domain. An empty value means the node has not declared a domain.
func (c *cloudRuntime) nodeFaultDomains() map[string]string {
	domains := map[string]string{c.id: c.faultDomain}
	c.mu.RLock()
	for _, peer := range c.peers {
		if peer.ID != "" {
			domains[peer.ID] = peer.FaultDomain
		}
	}
	c.mu.RUnlock()
	return domains
}

// completeCopyNodes returns the set of node IDs that hold every encrypted block
// of a record. Inventory hints prove addressability, not plaintext correctness;
// read-back verification is performed separately before release.
func (c *cloudClient) completeCopyNodes(record cloudRecord) map[string]bool {
	if len(record.BlockIDs) == 0 {
		return map[string]bool{c.runtime.id: true}
	}
	var complete map[string]bool
	for _, blockID := range record.BlockIDs {
		locations := map[string]bool{}
		for _, node := range c.runtime.blockLocations(blockID) {
			if node.ID != "" {
				locations[node.ID] = true
			}
		}
		if c.runtime.disk.has("blocks", blockID) {
			locations[c.runtime.id] = true
		}
		if complete == nil {
			complete = locations
			continue
		}
		for nodeID := range complete {
			if !locations[nodeID] {
				delete(complete, nodeID)
			}
		}
	}
	if complete == nil {
		complete = map[string]bool{}
	}
	return complete
}

// replicaDomains collapses complete-copy nodes into independent fault domains.
// Nodes without a declared domain are unknown and never counted. The returned
// slice is sorted for stable reporting.
func (c *cloudClient) replicaDomains(record cloudRecord) ([]string, int) {
	complete := c.completeCopyNodes(record)
	domains := c.runtime.nodeFaultDomains()
	seen := map[string]bool{}
	for nodeID := range complete {
		domain := domains[nodeID]
		if domain == "" {
			continue
		}
		seen[domain] = true
	}
	result := make([]string, 0, len(seen))
	for domain := range seen {
		result = append(result, domain)
	}
	sort.Strings(result)
	return result, len(complete)
}
