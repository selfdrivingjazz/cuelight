package software

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/updates"
)

// NodeStatus is one node's software state for operators.
type NodeStatus struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
	// Running is the reported softwareVersion; empty when unknown.
	Running           string     `json:"running,omitempty"`
	Target            string     `json:"target,omitempty"`
	TargetRequestedAt *time.Time `json:"targetRequestedAt,omitempty"`
	// LatestAvailable is the newest imported release this node can install.
	LatestAvailable string `json:"latestAvailable,omitempty"`
	Status          string `json:"status"`
	// Detail explains failed and unsupported statuses.
	Detail        string `json:"detail,omitempty"`
	UpdateMethod  string `json:"updateMethod,omitempty"`
	Channel       string `json:"channel,omitempty"`
	OutputStandby bool   `json:"outputStandby"`
	Platform      string `json:"platform,omitempty"`
	Architecture  string `json:"architecture,omitempty"`
}

// NodeStatuses computes the software status of every non-revoked node.
func (s *Service) NodeStatuses(ctx context.Context) ([]NodeStatus, error) {
	nodes, err := s.Repository.ListSoftwareNodes(ctx)
	if err != nil {
		return nil, err
	}
	releases, err := s.Releases(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]NodeStatus, 0, len(nodes))
	for _, node := range nodes {
		if node.CredentialState == "revoked" {
			continue
		}
		statuses = append(statuses, s.status(node, releases))
	}
	return statuses, nil
}

// NodeStatus computes one node's software status.
func (s *Service) NodeStatus(ctx context.Context, nodeID string) (NodeStatus, error) {
	node, err := s.Repository.SoftwareNode(ctx, nodeID)
	if err != nil {
		return NodeStatus{}, err
	}
	releases, err := s.Releases(ctx)
	if err != nil {
		return NodeStatus{}, err
	}
	return s.status(node, releases), nil
}

// status derives the operator-facing state from what the node reports, the
// operator's target, and the imported releases (newest first).
func (s *Service) status(node NodeRecord, releases []Release) NodeStatus {
	status := NodeStatus{NodeID: node.ID, NodeName: node.Name}
	if node.Target != nil {
		status.Target = node.Target.Version
		requestedAt := node.Target.RequestedAt
		status.TargetRequestedAt = &requestedAt
	}
	report := node.Report
	if report != nil {
		status.Running, status.UpdateMethod, status.Channel = report.SoftwareVersion, report.UpdateMethod, report.Channel
		status.OutputStandby, status.Platform, status.Architecture = report.OutputStandby, report.Platform, report.Architecture
	}
	if report != nil && report.UpdateMethod == MethodBrowser {
		// Browser nodes load the website-node build control serves and reload
		// themselves when control's version changes.
		status.LatestAvailable = s.ControlVersion
		switch {
		case status.Running == "":
			status.Status = StatusUnknown
		case status.Running == s.ControlVersion:
			status.Status = StatusCurrent
		default:
			status.Status = StatusUpdating
		}
		return status
	}
	for _, release := range releases {
		if channelAllows(status.Channel, release.Channel) && installable(release, report) {
			status.LatestAvailable = release.Version
			break
		}
	}
	if _, err := updates.ParseVersion(status.Running); err != nil {
		status.Status = StatusUnknown
		return status
	}
	if status.UpdateMethod == "" || status.UpdateMethod == MethodNone {
		status.Status, status.Detail = StatusUnsupported, "this node does not install software updates"
		return status
	}
	if status.Target != "" {
		if order, ok := compare(status.Target, status.Running); ok && order > 0 {
			if failure := node.Failure; failure != nil && failure.Version == status.Target && !failure.At.Before(node.Target.RequestedAt) {
				status.Status, status.Detail = StatusFailed, failure.Error
				return status
			}
			status.Status = StatusUpdating
			return status
		}
	}
	if status.LatestAvailable != "" {
		if order, ok := compare(status.LatestAvailable, status.Running); ok && order > 0 {
			status.Status = StatusOutdated
			return status
		}
	}
	status.Status = StatusCurrent
	return status
}

// SetTarget asks a node to run version. It never accepts a version below the
// one the node runs, and requires an installable release for the node.
func (s *Service) SetTarget(ctx context.Context, actorID, nodeID, version string) (NodeStatus, error) {
	node, err := s.Repository.SoftwareNode(ctx, nodeID)
	if err != nil {
		return NodeStatus{}, err
	}
	release, err := s.Repository.GetSoftwareRelease(ctx, version)
	if err != nil {
		return NodeStatus{}, err
	}
	if err := eligible(node, release); err != nil {
		return NodeStatus{}, err
	}
	if err := s.Repository.SetSoftwareTarget(ctx, nodeID, version, actorID); err != nil {
		return NodeStatus{}, err
	}
	return s.NodeStatus(ctx, nodeID)
}

func eligible(node NodeRecord, release Release) error {
	if node.CredentialState == "revoked" {
		return fmt.Errorf("%w: node is revoked", ErrInvalid)
	}
	report := node.Report
	if report == nil || report.SoftwareVersion == "" {
		return fmt.Errorf("%w: node has not reported its software version", ErrInvalid)
	}
	switch report.UpdateMethod {
	case MethodBrowser:
		return fmt.Errorf("%w: browser nodes follow the control server's version", ErrInvalid)
	case "", MethodNone:
		return fmt.Errorf("%w: node does not install software updates", ErrInvalid)
	}
	order, ok := compare(release.Version, report.SoftwareVersion)
	if !ok {
		return fmt.Errorf("%w: node reports an invalid software version %q", ErrInvalid, report.SoftwareVersion)
	}
	if order < 0 {
		return fmt.Errorf("%w: %s < %s", ErrDowngrade, release.Version, report.SoftwareVersion)
	}
	if !installable(release, report) {
		return fmt.Errorf("%w: release %s has no %s artifact for %s/%s", ErrInvalid, release.Version, report.UpdateMethod, report.Platform, report.Architecture)
	}
	return nil
}

// ClearTarget lets a node stay on whatever it runs.
func (s *Service) ClearTarget(ctx context.Context, actorID, nodeID string) (NodeStatus, error) {
	if err := s.Repository.ClearSoftwareTarget(ctx, nodeID, actorID); err != nil {
		return NodeStatus{}, err
	}
	return s.NodeStatus(ctx, nodeID)
}

// RolloutRequest targets version on the listed nodes, or on every node that
// can install it and runs an older version when AllEligible is set.
type RolloutRequest struct {
	Version     string   `json:"version"`
	NodeIDs     []string `json:"nodeIds,omitempty"`
	AllEligible bool     `json:"allEligible,omitempty"`
}

// RolloutSkip explains why a node was not targeted.
type RolloutSkip struct {
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}

// RolloutResult reports the targeted and skipped nodes.
type RolloutResult struct {
	Version string        `json:"version"`
	Updated []NodeStatus  `json:"updated"`
	Skipped []RolloutSkip `json:"skipped"`
}

// Rollout applies a target to many nodes.
func (s *Service) Rollout(ctx context.Context, actorID string, request RolloutRequest) (RolloutResult, error) {
	if (len(request.NodeIDs) > 0) == request.AllEligible {
		return RolloutResult{}, fmt.Errorf("%w: give either nodeIds or allEligible", ErrInvalid)
	}
	release, err := s.Repository.GetSoftwareRelease(ctx, request.Version)
	if err != nil {
		return RolloutResult{}, err
	}
	result := RolloutResult{Version: release.Version, Updated: []NodeStatus{}, Skipped: []RolloutSkip{}}
	nodeIDs := request.NodeIDs
	if request.AllEligible {
		nodes, err := s.Repository.ListSoftwareNodes(ctx)
		if err != nil {
			return RolloutResult{}, err
		}
		nodeIDs = nil
		for _, node := range nodes {
			if eligible(node, release) != nil {
				continue
			}
			if order, _ := compare(release.Version, node.Report.SoftwareVersion); order > 0 {
				nodeIDs = append(nodeIDs, node.ID)
			}
		}
	}
	for _, nodeID := range nodeIDs {
		status, err := s.SetTarget(ctx, actorID, nodeID, release.Version)
		if err != nil {
			if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrDowngrade) && !errors.Is(err, ErrNotFound) {
				return RolloutResult{}, err
			}
			result.Skipped = append(result.Skipped, RolloutSkip{NodeID: nodeID, Reason: err.Error()})
			continue
		}
		result.Updated = append(result.Updated, status)
	}
	return result, nil
}

// ControlStatus compares this control binary with the newest imported release.
type ControlStatus struct {
	Version         string `json:"version"`
	LatestAvailable string `json:"latestAvailable,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
}

// Overview is the Software page summary.
type Overview struct {
	Control ControlStatus `json:"control"`
	Feed    FeedStatus    `json:"feed"`
	// Latest is the newest imported release.
	Latest *Release `json:"-"`
}

// Overview summarizes control's version, the feed, and the newest release.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	releases, err := s.Releases(ctx)
	if err != nil {
		return Overview{}, err
	}
	feed, err := s.FeedStatus(ctx)
	if err != nil {
		return Overview{}, err
	}
	overview := Overview{Control: ControlStatus{Version: s.ControlVersion}, Feed: feed}
	if len(releases) > 0 {
		overview.Latest = &releases[0]
	}
	for _, release := range releases {
		if release.Channel != updates.ChannelStable {
			continue
		}
		overview.Control.LatestAvailable = release.Version
		order, ok := compare(release.Version, s.ControlVersion)
		overview.Control.UpdateAvailable = ok && order > 0
		break
	}
	return overview, nil
}
