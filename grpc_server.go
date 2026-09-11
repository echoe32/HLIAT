package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"backend/internal/grpc/pb"
	"backend/internal/models"
)

type grpcServer struct {
	pb.UnimplementedProjectServiceServer
	b *Backend
}

func (s *grpcServer) GetStats(ctx context.Context, req *pb.GetStatsRequest) (*pb.GetStatsResponse, error) {
	stats, err := s.b.GetStats(ctx)
	if err != nil {
		return nil, err
	}
	
	res := &pb.GetStatsResponse{}
	for _, st := range stats {
		res.Stats = append(res.Stats, &pb.CategoryStat{
			Date:          st.Date,
			Category:      st.Category,
			AverageApr:    float32(st.AverageAPR),
			AverageIncome: float32(st.AverageIncome),
		})
	}
	return res, nil
}

func (s *grpcServer) Fetch(ctx context.Context, req *pb.FetchRequest) (*pb.FetchResponse, error) {
	return &pb.FetchResponse{
		Message: "fetch endpoint deprecated, scheduler handles stats",
	}, nil
}

func (s *grpcServer) GetAllDeposits(ctx context.Context, req *pb.GetAllDepositsRequest) (*pb.GetAllDepositsResponse, error) {
	return &pb.GetAllDepositsResponse{
		Deposits: []*pb.Deposit{},
	}, nil
}

func (s *grpcServer) GetDepositByID(ctx context.Context, req *pb.GetDepositByIDRequest) (*pb.GetDepositByIDResponse, error) {
	return nil, fmt.Errorf("not found")
}

func (s *grpcServer) GetBonds(ctx context.Context, req *pb.GetBondsRequest) (*pb.GetBondsResponse, error) {
	board := req.Board
	if board == "" {
		board = "all"
	}

	var keys []string
	if board == "all" {
		for _, bb := range s.b.cfg.BondBoards {
			keys = append(keys, bb.RedisKey)
		}
	} else {
		for _, bb := range s.b.cfg.BondBoards {
			if bb.Label == board {
				keys = append(keys, bb.RedisKey)
				break
			}
		}
	}

	res := &pb.GetBondsResponse{}
	for _, key := range keys {
		bonds, _ := loadBonds(ctx, s.b.rdb, key)
		for _, b := range bonds {
			res.Bonds = append(res.Bonds, mapBond(b))
		}
	}
	return res, nil
}

func (s *grpcServer) Query(ctx context.Context, req *pb.QueryRequest) (*pb.QueryResponse, error) {
	resMap, err := processQuery(ctx, s.b.rdb, int(req.Sum), int(req.Count), s.b.cfg.BondBoards)
	if err != nil {
		return nil, err
	}
	
	resp := &pb.QueryResponse{
		Results: make(map[string]*pb.ProductList),
	}
	
	for k, v := range resMap {
		list := &pb.ProductList{}
		switch val := v.(type) {
		case []*ProductResult:
			for _, p := range val {
				if p != nil {
					list.Products = append(list.Products, mapProductResult(p))
				}
			}
		case []*BondResult:
			for _, p := range val {
				if p != nil {
					list.Products = append(list.Products, mapBondResult(p))
				}
			}
		}
		resp.Results[k] = list
	}
	return resp, nil
}

func mapProductResult(p *ProductResult) *pb.ProductResult {
	res := &pb.ProductResult{
		Type:       p.Type,
		Apr:        float32(p.APR),
		Earnings:   float32(p.Earnings),
		AmountFrom: int32(p.AmountFrom),
		Link:       p.Link,
	}
	if p.AmountTo != nil {
		res.AmountTo = int32(*p.AmountTo)
		res.HasAmountTo = true
	}
	return res
}

func mapBondResult(p *BondResult) *pb.ProductResult {
	res := &pb.ProductResult{
		Type:            p.Type,
		Secid:           p.SECID,
		ShortName:       p.ShortName,
		CouponPercent:   float32(p.CouponPercent),
		CouponValue:     float32(p.CouponValue),
		FaceValue:       float32(p.FaceValue),
		MatDate:         p.MatDate,
		Earnings:        float32(p.Earnings),
		Link:            p.Link,
	}
	if p.YieldToMaturity != nil {
		res.YieldToMaturity = float32(*p.YieldToMaturity)
	}
	if p.Last != nil {
		res.Last = float32(*p.Last)
	}
	return res
}

func mapDeposits(deps []models.Deposit) []*pb.Deposit {
	res := make([]*pb.Deposit, len(deps))
	for i, d := range deps {
		res[i] = mapDeposit(d)
	}
	return res
}

func mapDeposit(d models.Deposit) *pb.Deposit {
	res := &pb.Deposit{
		Id:                          int32(d.ID),
		ProductUrl:                  d.ProductURL,
		RateMin:                     float32(d.RateMin),
		RateMax:                     float32(d.RateMax),
		AmountFrom:                  int32(d.AmountFrom),
		PeriodFrom:                  int32(d.PeriodFrom),
		ProductName:                 d.ProductName,
		BankName:                    d.BankName,
		DepositName:                 d.DepositName,
		IsSavingAccount:             d.IsSavingAccount,
		IsChildrenDeposit:           d.IsChildrenDeposit,
		IsPensionDeposit:            d.IsPensionDeposit,
		FeatureList:                 d.FeatureList,
		EfficientRate:               float32(d.EfficientRate),
		CapitalizationPeriods:       d.CapitalizationPeriods,
		IsPartialWithdrawalPossible: d.IsPartialWithdrawalPossible,
		IsInOfficeOpeningPossible:   d.IsInOfficeOpeningPossible,
		IsRateIncreasePossible:      d.IsRateIncreasePossible,
		IsNewClient:                 d.IsNewClient,
		IsNewMoney:                  d.IsNewMoney,
		IsKeyRateLinked:             d.IsKeyRateLinked,
	}
	// Simplified mapping for brevity, normally you'd map all fields
	return res
}

func mapBond(b models.Bond) *pb.Bond {
	res := &pb.Bond{
		Secid:            b.SECID,
		BoardId:          b.BoardID,
		ShortName:        b.ShortName,
		Name:             b.SecName,
		FaceValue:        float32(b.FaceValue),
		CouponValue:      float32(b.CouponValue),
		CouponPercent:    float32(b.CouponPercent),
		CouponPeriod:     int32(b.CouponPeriod),
		AccruedInt:       float32(b.AccruedInt),
		MatDate:          b.MatDate,
		ListLevel:        strconv.Itoa(b.ListLevel),
	}
	if b.YieldToMaturity != nil {
		res.YieldToMaturity = float32(*b.YieldToMaturity)
		res.HasYieldToMaturity = true
	}
	if b.Last != nil {
		res.Last = float32(*b.Last)
		res.HasLast = true
	}
	return res
}

func startGRPCServer(b *Backend, port string) {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		// Log and return, no fatal to keep main running if needed
		return
	}
	s := grpc.NewServer()
	pb.RegisterProjectServiceServer(s, &grpcServer{b: b})
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		// Log
	}
}
