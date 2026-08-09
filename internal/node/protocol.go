package node

import "github.com/FrankoonG/x-tier/internal/route"

type OpenChainRequest struct {
	Version      int                `json:"version"`
	RequestID    string             `json:"request_id"`
	OriginNodeID route.NodeID       `json:"origin_node_id"`
	CurrentNode  route.NodeID       `json:"current_node_id"`
	Remaining    []route.NodeID     `json:"remaining_hops"`
	Terminal     route.NodeID       `json:"terminal"`
	EndpointKind route.EndpointKind `json:"endpoint_kind"`
}

type OpenChainResult struct {
	OK                 bool         `json:"ok"`
	FinalNodeID        route.NodeID `json:"final_node_id,omitempty"`
	TerminalInstanceID string       `json:"terminal_instance_id,omitempty"`
	ErrorCode          string       `json:"error_code,omitempty"`
	ErrorMessage       string       `json:"error_message,omitempty"`
	FailedHop          route.NodeID `json:"failed_hop,omitempty"`
}
