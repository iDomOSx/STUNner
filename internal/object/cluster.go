package object

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/pion/logging"

	"github.com/l7mp/stunner/internal/resolver"
	"github.com/l7mp/stunner/internal/util"
	"github.com/l7mp/stunner/pkg/apis/v1alpha1"
)

// Listener implements a STUNner cluster
type Cluster struct {
	Name      string
	Type      v1alpha1.ClusterType
	Endpoints []net.IPNet
	Domains   []string
	Resolver  resolver.DnsResolver // for strict DNS
	logger    logging.LoggerFactory
	log       logging.LeveledLogger
}

// NewCluster creates a new cluster. Requires a server restart (returns
// v1alpha1.ErrRestartRequired)
func NewCluster(conf v1alpha1.Config, resolver resolver.DnsResolver, logger logging.LoggerFactory) (Object, error) {
	req, ok := conf.(*v1alpha1.ClusterConfig)
	if !ok {
		return nil, v1alpha1.ErrInvalidConf
	}

	// make sure req.Name is correct
	if err := req.Validate(); err != nil {
		return nil, err
	}

	c := Cluster{
		Name:      req.Name,
		Endpoints: []net.IPNet{},
		Domains:   []string{},
		Resolver:  resolver,
		logger:    logger,
		log:       logger.NewLogger(fmt.Sprintf("stunner-cluster-%s", req.Name)),
	}

	c.log.Tracef("NewCluster: %#v", req)

	if err := c.Reconcile(req); err != nil && err != v1alpha1.ErrRestartRequired {
		return nil, err
	}

	return &c, nil
}

// Inspect examines whether a configuration change on the object would require a restart. An empty
// new-config means it is about to be deleted, an empty old-config means it is to be deleted,
// otherwise it will be reconciled from the old configuration to the new one
func (c *Cluster) Inspect(old, new v1alpha1.Config) bool {
	return false
}

// Reconcile updates the authenticator for a new configuration.
func (c *Cluster) Reconcile(conf v1alpha1.Config) error {
	req, ok := conf.(*v1alpha1.ClusterConfig)
	if !ok {
		return v1alpha1.ErrInvalidConf
	}

	if err := req.Validate(); err != nil {
		return err
	}

	c.log.Tracef("Reconcile: %#v", req)
	c.Type, _ = v1alpha1.NewClusterType(req.Type)

	switch c.Type {
	case v1alpha1.ClusterTypeStatic:
		// remove existing endpoints and start anew
		c.Endpoints = c.Endpoints[:0]
		for _, e := range req.Endpoints {
			// try to parse as a subnet
			_, n, err := net.ParseCIDR(e)
			if err == nil {
				c.Endpoints = append(c.Endpoints, *n)
				continue
			}

			// try to parse as an IP address
			a := net.ParseIP(e)
			if a == nil {
				c.log.Warnf("cluster %q: invalid endpoint IP: %q, ignoring", c.Name, e)
				continue
			}

			// add a prefix and reparse
			if a.To4() == nil {
				e = e + "/128"
			} else {
				e = e + "/32"
			}

			_, n2, err := net.ParseCIDR(e)
			if err != nil {
				c.log.Warnf("cluster %q: could not convert endpoint %q to CIDR subnet ",
					"(ignoring): %s", c.Name, e, err.Error())
				continue
			}

			c.Endpoints = append(c.Endpoints, *n2)
		}
	case v1alpha1.ClusterTypeStrictDNS:
		if c.Resolver == nil {
			return fmt.Errorf("STRICT_DNS cluster %q initialized with no DNS resolver", c.Name)
		}

		deleted, added := util.Diff(c.Domains, req.Endpoints)

		for _, h := range deleted {
			c.Resolver.Unregister(h)
			c.Domains = util.Remove(c.Domains, h)
		}

		for _, h := range added {
			if c.Resolver.Register(h) == nil {
				c.Domains = append(c.Domains, h)
			}
		}
	}

	return nil
}

// Name returns the name of the object
func (c *Cluster) ObjectName() string {
	// singleton!
	return c.Name
}

// GetConfig returns the configuration of the running cluster
func (c *Cluster) GetConfig() v1alpha1.Config {
	conf := v1alpha1.ClusterConfig{
		Name: c.Name,
		Type: c.Type.String(),
	}

	switch c.Type {
	case v1alpha1.ClusterTypeStatic:
		conf.Endpoints = make([]string, len(c.Endpoints))
		for i, e := range c.Endpoints {
			conf.Endpoints[i] = e.String()
		}
	case v1alpha1.ClusterTypeStrictDNS:
		conf.Endpoints = make([]string, len(c.Domains))
		copy(conf.Endpoints, c.Domains)
		conf.Endpoints = sort.StringSlice(conf.Endpoints)
	}
	return &conf
}

// Close closes the cluster
func (c *Cluster) Close() error {
	c.log.Trace("closing cluster")

	switch c.Type {
	case v1alpha1.ClusterTypeStatic:
		// do nothing
	case v1alpha1.ClusterTypeStrictDNS:
		for _, d := range c.Domains {
			c.Resolver.Unregister(d)
		}
	}

	return nil
}

// Route decides whwther a peer IP appears among the permitted endpoints of a cluster
func (c *Cluster) Route(peer net.IP) bool {
	c.log.Tracef("Route: cluster %q of type %s, peer IP: %s", c.Name, c.Type.String(),
		peer.String())

	switch c.Type {
	case v1alpha1.ClusterTypeStatic:
		// endpoints are IPNets
		for _, e := range c.Endpoints {
			c.log.Tracef("considering endpoint %q", e)
			if e.Contains(peer) {
				return true
			}
		}

	case v1alpha1.ClusterTypeStrictDNS:
		// endpoints are obtained from the DNS
		c.log.Tracef("running STRICT_DNS cluster with domains: [%s]", strings.Join(c.Domains, ", "))

		for _, d := range c.Domains {
			c.log.Tracef("considering domain %q", d)

			hs, err := c.Resolver.Lookup(d)
			if err != nil {
				c.log.Infof("could not resolve domain %q: %s", d, err.Error())
			}

			for _, n := range hs {
				c.log.Tracef("considering IP address %q", n)

				if n.Equal(peer) {
					return true
				}
			}
		}
	}

	return false
}

// ClusterFactory can create now Cluster objects
type ClusterFactory struct {
	resolver resolver.DnsResolver
	logger   logging.LoggerFactory
}

// NewClusterFactory creates a new factory for Cluster objects
func NewClusterFactory(resolver resolver.DnsResolver, logger logging.LoggerFactory) Factory {
	return &ClusterFactory{resolver: resolver, logger: logger}
}

// New can produce a new Cluster object from the given configuration. A nil config will create an
// empty cluster object (useful for creating throwaway objects for, e.g., calling Inpect)
func (f *ClusterFactory) New(conf v1alpha1.Config) (Object, error) {
	if conf == nil {
		return &Cluster{}, nil
	}

	return NewCluster(conf, f.resolver, f.logger)
}
