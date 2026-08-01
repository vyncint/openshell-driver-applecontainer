// Package grpcsvc implements the openshell.compute.v1.ComputeDriver service
// backed by apple/container VMs.
package grpcsvc

import (
	"context"
	"log/slog"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
	"github.com/vyncint/openshell-driver-applecontainer/internal/state"
)

// DriverName is the human-readable identity reported via GetCapabilities.
// Routing uses the gateway-side config key, not this string.
const DriverName = "applecontainer"

// entry is the in-memory registry record for one sandbox.
type entry struct {
	rec      state.Record
	cond     condition
	deleting bool
}

// Server implements computev1.ComputeDriverServer.
type Server struct {
	computev1.UnimplementedComputeDriverServer

	cfg     config.Config
	rt      backend.Runtime
	store   *state.Store
	log     *slog.Logger
	version string
	hub     *hub

	mu        sync.Mutex
	sandboxes map[string]*entry
}

// New builds a Server. Version is the build version injected by the linker.
func New(cfg config.Config, rt backend.Runtime, store *state.Store, log *slog.Logger, version string) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:       cfg,
		rt:        rt,
		store:     store,
		log:       log,
		version:   version,
		hub:       newHub(log),
		sandboxes: make(map[string]*entry),
	}
}

func (s *Server) GetCapabilities(_ context.Context, _ *computev1.GetCapabilitiesRequest) (*computev1.GetCapabilitiesResponse, error) {
	return &computev1.GetCapabilitiesResponse{
		DriverName:    DriverName,
		DriverVersion: s.version,
		DefaultImage:  s.cfg.DefaultImage,
	}, nil
}

// validateSandbox holds the checks shared by ValidateSandboxCreate and
// CreateSandbox.
func (s *Server) validateSandbox(sb *computev1.DriverSandbox) error {
	if sb == nil {
		return status.Error(codes.InvalidArgument, "sandbox is required")
	}
	if !state.ValidID(sb.GetId()) {
		return status.Errorf(codes.InvalidArgument, "invalid sandbox id %q", sb.GetId())
	}
	if gpu := sb.GetSpec().GetResourceRequirements().GetGpu(); gpu != nil {
		return status.Error(codes.InvalidArgument, "gpu sandboxes are not supported by the applecontainer driver")
	}
	return nil
}

func (s *Server) ValidateSandboxCreate(_ context.Context, req *computev1.ValidateSandboxCreateRequest) (*computev1.ValidateSandboxCreateResponse, error) {
	if err := s.validateSandbox(req.GetSandbox()); err != nil {
		return nil, err
	}
	return &computev1.ValidateSandboxCreateResponse{}, nil
}

func (s *Server) CreateSandbox(_ context.Context, req *computev1.CreateSandboxRequest) (*computev1.CreateSandboxResponse, error) {
	sb := req.GetSandbox()
	s.log.Info("create sandbox requested",
		"sandbox_id", sb.GetId(),
		"name", sb.GetName(),
		"workspace", sb.GetWorkspace(),
		"image", sb.GetSpec().GetTemplate().GetImage(),
	)
	if err := s.validateSandbox(sb); err != nil {
		return nil, err
	}
	return nil, status.Error(codes.Unimplemented, "CreateSandbox is not implemented yet")
}

func (s *Server) GetSandbox(_ context.Context, req *computev1.GetSandboxRequest) (*computev1.GetSandboxResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookupLocked(req.GetSandboxId(), req.GetSandboxName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %q not found", req.GetSandboxId())
	}
	return &computev1.GetSandboxResponse{Sandbox: s.snapshotLocked(e)}, nil
}

func (s *Server) ListSandboxes(_ context.Context, _ *computev1.ListSandboxesRequest) (*computev1.ListSandboxesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &computev1.ListSandboxesResponse{}
	for _, e := range s.sandboxes {
		resp.Sandboxes = append(resp.Sandboxes, s.snapshotLocked(e))
	}
	return resp, nil
}

func (s *Server) StopSandbox(_ context.Context, _ *computev1.StopSandboxRequest) (*computev1.StopSandboxResponse, error) {
	return nil, status.Error(codes.Unimplemented, "StopSandbox is not implemented yet")
}

func (s *Server) DeleteSandbox(_ context.Context, req *computev1.DeleteSandboxRequest) (*computev1.DeleteSandboxResponse, error) {
	s.log.Info("delete sandbox requested", "sandbox_id", req.GetSandboxId(), "name", req.GetSandboxName())
	return nil, status.Error(codes.Unimplemented, "DeleteSandbox is not implemented yet")
}

func (s *Server) WatchSandboxes(_ *computev1.WatchSandboxesRequest, stream computev1.ComputeDriver_WatchSandboxesServer) error {
	// Subscribe before snapshotting so no transition between the two is lost;
	// duplicates are harmless (the gateway applies snapshots idempotently).
	id, ch := s.hub.subscribe()
	defer s.hub.unsubscribe(id)

	s.mu.Lock()
	snapshots := make([]*computev1.DriverSandbox, 0, len(s.sandboxes))
	for _, e := range s.sandboxes {
		snapshots = append(snapshots, s.snapshotLocked(e))
	}
	s.mu.Unlock()

	for _, sb := range snapshots {
		ev := &computev1.WatchSandboxesEvent{
			Payload: &computev1.WatchSandboxesEvent_Sandbox{
				Sandbox: &computev1.WatchSandboxesSandboxEvent{Sandbox: sb},
			},
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// lookupLocked resolves an entry by id, falling back to the sandbox name.
func (s *Server) lookupLocked(id, name string) (*entry, bool) {
	if e, ok := s.sandboxes[id]; ok {
		return e, true
	}
	if name == "" {
		return nil, false
	}
	for _, e := range s.sandboxes {
		if e.rec.Name == name {
			return e, true
		}
	}
	return nil, false
}

// snapshotLocked renders the current driver-native observation for e.
func (s *Server) snapshotLocked(e *entry) *computev1.DriverSandbox {
	return &computev1.DriverSandbox{
		Id:        e.rec.ID,
		Name:      e.rec.Name,
		Namespace: s.cfg.Namespace,
		Workspace: e.rec.Workspace,
		Status: &computev1.DriverSandboxStatus{
			SandboxName: e.rec.ContainerName,
			InstanceId:  e.rec.ContainerName,
			Conditions:  []*computev1.DriverCondition{e.cond.proto()},
			Deleting:    e.deleting,
		},
	}
}

// publishSandbox emits a watch event with the sandbox's current snapshot.
func (s *Server) publishSandbox(e *entry) {
	s.mu.Lock()
	sb := s.snapshotLocked(e)
	s.mu.Unlock()
	s.hub.publish(&computev1.WatchSandboxesEvent{
		Payload: &computev1.WatchSandboxesEvent_Sandbox{
			Sandbox: &computev1.WatchSandboxesSandboxEvent{Sandbox: sb},
		},
	})
}

// publishDeleted emits a deletion watch event.
func (s *Server) publishDeleted(id string) {
	s.hub.publish(&computev1.WatchSandboxesEvent{
		Payload: &computev1.WatchSandboxesEvent_Deleted{
			Deleted: &computev1.WatchSandboxesDeletedEvent{SandboxId: id},
		},
	})
}
