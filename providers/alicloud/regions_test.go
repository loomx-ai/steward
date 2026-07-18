package alicloud

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alibabacloud-go/tea/dara"
	vpcclient "github.com/alibabacloud-go/vpc-20160428/v7/client"
)

type vpcRegionCallerStub struct {
	request  *vpcclient.DescribeRegionsRequest
	runtime  *dara.RuntimeOptions
	timeout  time.Duration
	calls    int
	failures int
}

func (s *vpcRegionCallerStub) DescribeRegionsWithContext(ctx context.Context, request *vpcclient.DescribeRegionsRequest, runtime *dara.RuntimeOptions) (*vpcclient.DescribeRegionsResponse, error) {
	s.request = request
	s.runtime = runtime
	s.calls++
	if deadline, ok := ctx.Deadline(); ok {
		s.timeout = time.Until(deadline)
	}
	if s.failures > 0 {
		s.failures--
		return nil, errors.New("temporary VPC failure")
	}
	return &vpcclient.DescribeRegionsResponse{Body: &vpcclient.DescribeRegionsResponseBody{
		Regions: &vpcclient.DescribeRegionsResponseBodyRegions{Region: []*vpcclient.DescribeRegionsResponseBodyRegionsRegion{
			{RegionId: dara.String("cn-hangzhou"), LocalName: dara.String("华东 1（杭州）"), RegionEndpoint: dara.String("vpc.cn-hangzhou.aliyuncs.com")},
		}},
	}}, nil
}

func TestDiscoverRegionsUsesChineseVPCRegionCatalog(t *testing.T) {
	client := &vpcRegionCallerStub{}
	regions, err := discoverRegionsWithVPC(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if client.request == nil || dara.StringValue(client.request.AcceptLanguage) != "zh-CN" {
		t.Fatalf("request = %#v", client.request)
	}
	if client.calls != 1 ||
		dara.IntValue(client.runtime.ReadTimeout) != int(cloudProductQueryTimeout.Milliseconds()) ||
		client.timeout <= 4*time.Second ||
		client.timeout > cloudProductQueryTimeout {
		t.Fatalf("calls=%d runtime=%#v timeout=%v", client.calls, client.runtime, client.timeout)
	}
	if len(regions) != 1 || regions[0].RegionID != "cn-hangzhou" || regions[0].Name != "华东 1（杭州）" || regions[0].Endpoint != "vpc.cn-hangzhou.aliyuncs.com" {
		t.Fatalf("regions = %#v", regions)
	}
}

func TestDiscoverRegionsRetriesThreeTimes(t *testing.T) {
	client := &vpcRegionCallerStub{failures: cloudProductQueryRetryCount}
	if _, err := discoverRegionsWithVPC(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if client.calls != cloudProductQueryRetryCount+1 {
		t.Fatalf("calls=%d want=%d", client.calls, cloudProductQueryRetryCount+1)
	}
}
