package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func validGuestRoot(root stream.RootIdentity) bool {
	return len(root.Path) >= 4 && root.Path[1] == ':' && root.Path[2] == '\\' && !strings.ContainsAny(root.Path, "/\x00") && domain.ValidateBoundedString(root.Path, 4, 1024, ErrGuestOwnership) == nil && root.Volume > 0 && len(root.FileID) == 32 && validLabDigest(root.FileID+root.FileID) && root.CreationTime > 0 && root.Owner != "" && validLabDigest(root.SDDLHash)
}
func validGuestWorker(worker stream.WorkerIdentity) bool {
	return worker.PID > 0 && worker.BirthTicks > 0 && domain.ValidateBoundedString(worker.Image, 1, 1024, ErrGuestOwnership) == nil && domain.ValidateBoundedString(worker.SID, 1, 256, ErrGuestOwnership) == nil
}
func guestFileRoot(file stream.FileIdentity) stream.RootIdentity {
	return stream.RootIdentity{Path: file.RootPath, Volume: file.RootVolume, FileID: file.RootFileID, CreationTime: file.RootCreationTime, Owner: file.RootOwner, SDDLHash: file.RootSDDLHash}
}
func validGuestFile(file stream.FileIdentity, root stream.RootIdentity) bool {
	return guestFileRoot(file) == root && file.Volume == root.Volume && len(file.FileID) == 32 && validLabDigest(file.FileID+file.FileID) && file.CreationTime > 0 && file.Owner == root.Owner && validLabDigest(file.SDDLHash) && file.Size >= 0 && validLabDigest(file.SHA256)
}

func (s *GuestService) run(ctx context.Context, p GuestOperationParams, op domain.Operation, config GuestConfiguration, key, id string, input io.Reader, output io.Writer, effect *bool) (out GuestOperationResult, runErr error) {
	attempted := false
	var worker guestWorkerRecord
	defer func() {
		if runErr != nil && attempted {
			runErr = errors.Join(runErr, s.retireWorker(ctx, key, worker))
		}
	}()
	if err := s.checkRetirement(ctx, key, guestWorkerRecord{Target: op.Target, Caller: op.Actor.AuthenticatedCaller, Actor: op.Actor.EffectiveActor, Generation: config.Generation}); err != nil {
		return out, err
	}
	if p.Operation == "prepare" {
		*effect = true
		worker = guestWorkerRecord{Target: op.Target, Caller: op.Actor.AuthenticatedCaller, Actor: op.Actor.EffectiveActor, Generation: config.Generation, Root: config.WorkspaceIdentity, RecordID: id}
		if err := s.recordLaunch(ctx, key, worker); err != nil {
			return out, err
		}
		attempted = true
		identity, err := s.transport.Prepare(ctx, op.Target, key, config.Generation)
		if err != nil {
			return out, ErrGuestUncertain
		}
		worker = guestWorkerRecord{Target: op.Target, Caller: op.Actor.AuthenticatedCaller, Actor: op.Actor.EffectiveActor, Generation: config.Generation, Identity: identity, Root: config.WorkspaceIdentity, RecordID: id}
		if !validGuestWorker(identity) || identity.SID != config.WorkspaceIdentity.Owner {
			return out, guestProtocolError()
		}
		if err = s.store.write(ctx, id, ".worker", worker); err != nil {
			return out, ErrGuestUncertain
		}
		if err = s.recordReady(ctx, key, worker); err != nil {
			return out, err
		}
	} else {
		s.mu.Lock()
		worker = s.workers[key]
		s.mu.Unlock()
		if worker.RecordID == "" {
			return out, ErrGuestNotPrepared
		}
		var persisted guestWorkerRecord
		if err := s.store.read(ctx, worker.RecordID, ".worker", &persisted); err != nil || !reflect.DeepEqual(persisted, worker) {
			return out, ErrGuestOwnership
		}
	}
	if worker.Target != op.Target || worker.Actor != op.Actor.EffectiveActor || worker.Caller != op.Actor.AuthenticatedCaller || worker.Generation != config.Generation || worker.Root != config.WorkspaceIdentity {
		return out, ErrGuestAuthority
	}
	request, owned, err := s.request(ctx, p, op, config, id)
	if err != nil {
		return out, err
	}
	// Prepare and ownership reads can take time. Refresh the exact server authority
	// again after their durable writes, immediately before the transport effect.
	if err := s.validateEffectAuthority(ctx, p, config); err != nil {
		return out, err
	}
	*effect = true // Download dispatch/exposure is an admitted effect too.
	attempted = true
	result, err := s.transport.Execute(ctx, op.Target, key, config.Generation, request, input, output)
	if err != nil {
		return out, ErrGuestUncertain
	}
	if result.ID != id || result.Status != "ok" || result.Worker != worker.Identity {
		return out, guestProtocolError()
	}
	if checked, ok := input.(*guestCheckedInput); ok && !checked.valid() {
		return out, stream.ErrIntegrity
	}
	out.Worker = result.Worker
	out.Size = result.Size
	out.SHA256 = result.SHA256
	out.ExitCode = result.ExitCode
	if spool, ok := output.(*guestSpool); ok && !spool.matches(out) {
		return out, stream.ErrIntegrity
	}
	switch p.Operation {
	case "prepare":
		if result.Root == nil || *result.Root != config.WorkspaceIdentity {
			return out, guestProtocolError()
		}
		s.mu.Lock()
		s.workers[key] = worker
		s.mu.Unlock()
	case "put":
		if len(result.Files) != 1 {
			return out, guestProtocolError()
		}
		file := result.Files[0]
		if !validGuestFile(file, config.WorkspaceIdentity) || file.Path != request.Path || file.Size != p.Size || file.SHA256 != p.SHA256 {
			return out, guestProtocolError()
		}
		artifact := GuestArtifact{ID: id[:32], Size: file.Size, SHA256: file.SHA256}
		record := guestOwnedArtifact{Artifact: artifact, Target: op.Target, Caller: op.Actor.AuthenticatedCaller, Actor: op.Actor.EffectiveActor, Generation: config.Generation, Identity: file}
		if err = s.store.write(ctx, guestArtifactStoreID(artifact.ID), ".artifact", record); err != nil {
			return out, ErrGuestUncertain
		}
		out.Artifacts = []GuestArtifact{artifact}
		out.Size = file.Size
		out.SHA256 = file.SHA256
	case "get":
		if len(result.Files) != 1 || result.Files[0] != owned[0].Identity || result.Size != owned[0].Artifact.Size || result.SHA256 != owned[0].Artifact.SHA256 {
			return out, guestProtocolError()
		}
	case "exec":
		if result.Size < 0 || result.Size > p.MaxOutput || !validLabDigest(result.SHA256) {
			return out, guestProtocolError()
		}
	case "remove-owned":
		if len(result.Removed) != len(owned) {
			return out, guestProtocolError()
		}
		for i, removed := range result.Removed {
			if removed.Path != owned[i].Identity.Path {
				return out, guestProtocolError()
			}
			switch removed.Status {
			case "deleted", "preserved", "absence_unconfirmed", "expired":
			default:
				return out, guestProtocolError()
			}
			if removed.Status == "deleted" && !removed.Absent {
				return out, guestProtocolError()
			}
			out.Removed = append(out.Removed, GuestRemoval{ArtifactID: owned[i].Artifact.ID, Attempted: true, Accepted: removed.Status == "deleted", Absent: removed.Absent, Status: removed.Status})
		}
	}
	return out, nil
}

func (s *GuestService) request(ctx context.Context, p GuestOperationParams, op domain.Operation, config GuestConfiguration, id string) (stream.Request, []guestOwnedArtifact, error) {
	request := stream.Request{ID: id, Operation: p.Operation, DeadlineUTC: op.Deadline, Size: p.Size, SHA256: p.SHA256, MaxOutput: p.MaxOutput}
	var owned []guestOwnedArtifact
	for _, artifactID := range p.ArtifactIDs {
		var record guestOwnedArtifact
		if err := s.store.read(ctx, guestArtifactStoreID(artifactID), ".artifact", &record); err != nil {
			return request, nil, ErrGuestOwnership
		}
		if record.Artifact.ID != artifactID || record.Target != op.Target || record.Caller != op.Actor.AuthenticatedCaller || record.Actor != op.Actor.EffectiveActor || record.Generation != config.Generation || !validGuestFile(record.Identity, config.WorkspaceIdentity) || record.Identity.Path != guestArtifactPath(config.WorkspaceIdentity.Path, artifactID) || record.Artifact.Size != record.Identity.Size || record.Artifact.SHA256 != record.Identity.SHA256 {
			return request, nil, ErrGuestOwnership
		}
		owned = append(owned, record)
		request.Owned = append(request.Owned, record.Identity)
	}
	switch p.Operation {
	case "prepare":
		request.Operation = "observe"
		request.Path = config.WorkspaceIdentity.Path
	case "put":
		request.Path = guestArtifactPath(config.WorkspaceIdentity.Path, id[:32])
		root := config.WorkspaceIdentity
		request.ExpectedRoot = &root
	case "get":
		request.Path = owned[0].Identity.Path
		if p.MaxOutput < owned[0].Artifact.Size {
			return request, nil, errors.New("app: guest download exceeds output budget")
		}
	}
	return request, owned, nil
}
