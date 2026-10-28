package render

import (
	"encoding/json"

	"github.com/aetertinas9/data-path-assurance/internal/datapath"
)

type jsonLink struct {
	Known           bool    `json:"known"`
	Gen             int     `json:"gen,omitempty"`
	Width           int     `json:"width,omitempty"`
	ExpectedGen     int     `json:"expected_gen,omitempty"`
	ExpectedWidth   int     `json:"expected_width,omitempty"`
	MaxGen          int     `json:"max_gen,omitempty"`
	MaxWidth        int     `json:"max_width,omitempty"`
	GBps            float64 `json:"gbps,omitempty"`
	Degraded        bool    `json:"degraded"`
	PlatformLimited bool    `json:"platform_limited"`
}

type jsonHop struct {
	BDF   string   `json:"bdf"`
	Kind  string   `json:"kind"`
	Model string   `json:"model"`
	Link  jsonLink `json:"link"`
	ACS   string   `json:"acs,omitempty"`
}

type jsonNIC struct {
	BDF        string   `json:"bdf"`
	Model      string   `json:"model"`
	DPU        bool     `json:"dpu"`
	Port       string   `json:"port,omitempty"`
	PortGbps   int      `json:"port_gbps,omitempty"`
	PortState  string   `json:"port_state,omitempty"`
	NUMA       int      `json:"numa"`
	Locality   string   `json:"locality"`
	TopoRel    string   `json:"topo,omitempty"`
	Capacity   float64  `json:"capacity_gbps"`
	Bottleneck string   `json:"bottleneck"`
	Hops       []string `json:"hops"`
}

type jsonPeer struct {
	BDF      string   `json:"bdf"`
	Locality string   `json:"locality"`
	TopoRel  string   `json:"topo,omitempty"`
	NVLinks  int      `json:"nvlinks,omitempty"`
	ACSOn    []string `json:"acs_redirect_on,omitempty"`
	ACSKnown bool     `json:"acs_known"`
}

type jsonCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type jsonGPU struct {
	BDF     string      `json:"bdf"`
	Index   int         `json:"index"`
	UUID    string      `json:"uuid,omitempty"`
	Name    string      `json:"name"`
	NUMA    int         `json:"numa"`
	CPUList string      `json:"cpus,omitempty"`
	Path    []jsonHop   `json:"path"`
	NICs    []jsonNIC   `json:"nics"`
	Peers   []jsonPeer  `json:"peers"`
	NVLink  *jsonNVLink `json:"nvlink,omitempty"`
	Checks  []jsonCheck `json:"checks"`
	Result  string      `json:"result"`
}

type jsonNVLink struct {
	Active int `json:"active"`
	Total  int `json:"total"`
}

type jsonReport struct {
	Source      string     `json:"source"`
	Result      string     `json:"result"`
	NUMA        []jsonNUMA `json:"numa"`
	GPUs        []jsonGPU  `json:"gpus"`
	Limitations []string   `json:"limitations"`
}

type jsonNUMA struct {
	ID   int    `json:"id"`
	CPUs string `json:"cpus"`
}

func toLink(l datapath.LinkState) jsonLink {
	return jsonLink{Known: l.Known, Gen: datapath.Gen(l.Speed), Width: l.Width,
		ExpectedGen: datapath.Gen(l.ExpSpeed), ExpectedWidth: l.ExpWidth,
		MaxGen: datapath.Gen(l.MaxSpeed), MaxWidth: l.MaxWidth, GBps: l.GBps,
		Degraded: l.Degraded, PlatformLimited: l.PlatformLimited}
}

// JSON renders the report as indented JSON.
func JSON(r *datapath.Report) ([]byte, error) {
	out := jsonReport{Source: r.Source, Result: r.Overall().String(), Limitations: r.Limitations}
	if out.Limitations == nil {
		out.Limitations = []string{}
	}
	for _, n := range r.NUMA {
		out.NUMA = append(out.NUMA, jsonNUMA{n.ID, n.CPUList})
	}
	for _, g := range r.GPUs {
		jg := jsonGPU{BDF: g.Dev.BDF, Index: g.Index, UUID: g.UUID, Name: g.Name, NUMA: g.NUMA, CPUList: g.CPUList, Result: worst(g)}
		for _, h := range g.Hops {
			jh := jsonHop{BDF: h.Dev.BDF, Kind: h.Kind, Model: h.Dev.Model, Link: toLink(h.Dev.Link)}
			if h.Kind != "gpu" && h.Kind != "root" {
				jh.ACS = acs(h.Dev)
			}
			jg.Path = append(jg.Path, jh)
		}
		for _, a := range g.NICs {
			jn := jsonNIC{BDF: a.NIC.Dev.BDF, Model: a.NIC.Dev.Model, DPU: a.NIC.IsDPU, Port: a.NIC.Port, PortGbps: a.NIC.PortGbps,
				PortState: a.NIC.PortState, NUMA: a.NIC.Dev.Raw.NUMA, Locality: a.Locality.String(), TopoRel: a.TopoRel,
				Capacity: a.Capacity, Bottleneck: a.Bottleneck, Hops: a.Hops}
			if jn.Hops == nil {
				jn.Hops = []string{}
			}
			jg.NICs = append(jg.NICs, jn)
		}
		for _, p := range g.Peers {
			jg.Peers = append(jg.Peers, jsonPeer{BDF: p.GPU.Dev.BDF, Locality: p.Locality.String(), TopoRel: p.TopoRel, NVLinks: p.NVLinks, ACSOn: p.ACSOn, ACSKnown: p.ACSKnown})
		}
		if g.NVLink != nil && len(g.NVLink.Links) > 0 {
			jg.NVLink = &jsonNVLink{Active: g.NVLink.Active, Total: len(g.NVLink.Links)}
		}
		for _, c := range g.Checks {
			jg.Checks = append(jg.Checks, jsonCheck{c.Name, c.Status.String(), c.Detail})
		}
		if jg.NICs == nil {
			jg.NICs = []jsonNIC{}
		}
		if jg.Peers == nil {
			jg.Peers = []jsonPeer{}
		}
		out.GPUs = append(out.GPUs, jg)
	}
	if out.GPUs == nil {
		out.GPUs = []jsonGPU{}
	}
	if out.NUMA == nil {
		out.NUMA = []jsonNUMA{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
