package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"alfa.local/scoring-service/internal/scoring"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	rpc.UnimplementedScoringServiceServer
	Config scoring.Config
}

func Decode(payload []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return status.Error(codes.InvalidArgument, "expected one JSON document")
	}
	return nil
}
func (s *Server) RankEvents(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if err := platform.RequireCompany(ctx, r.CompanyId); err != nil {
		return nil, err
	}
	if !platform.Principal(ctx).Service {
		return nil, status.Error(codes.PermissionDenied, "service credentials required for scoring snapshots")
	}
	var input scoring.Request
	if err := Decode(r.Payload, &input); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid scoring JSON")
	}
	if input.Company.ID != r.CompanyId || input.Profile.CompanyID != r.CompanyId {
		return nil, status.Error(codes.PermissionDenied, "company mismatch")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	output, err := s.Config.Rank(input)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	data, err := json.Marshal(output)
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid scoring result")
	}
	return &rpc.Response{Payload: data}, nil
}
