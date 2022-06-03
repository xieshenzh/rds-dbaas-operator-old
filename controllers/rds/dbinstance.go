/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rds

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

type DescribeDBInstancesPaginatorAPI interface {
	HasMorePages() bool
	NextPage(context.Context, ...func(option *rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}

type sdkV2DescribeDBInstancesPaginator struct {
	paginator *rds.DescribeDBInstancesPaginator
}

func NewDescribeDBInstancesPaginator(accessKey, secretKey, region string) DescribeDBInstancesPaginatorAPI {
	awsClient := rds.New(rds.Options{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	})
	paginator := rds.NewDescribeDBInstancesPaginator(awsClient, nil)
	return &sdkV2DescribeDBInstancesPaginator{
		paginator: paginator,
	}
}

func (p *sdkV2DescribeDBInstancesPaginator) HasMorePages() bool {
	return p.paginator.HasMorePages()
}

func (p *sdkV2DescribeDBInstancesPaginator) NextPage(ctx context.Context, f ...func(option *rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	return p.paginator.NextPage(ctx, f...)
}

type ModifyDBInstanceAPI interface {
	ModifyDBInstance(ctx context.Context, params *rds.ModifyDBInstanceInput, optFns ...func(*rds.Options)) (*rds.ModifyDBInstanceOutput, error)
}

type sdkV2ModifyDBInstance struct {
	client *rds.Client
}

func NewModifyDBInstance(accessKey, secretKey, region string) ModifyDBInstanceAPI {
	awsClient := rds.New(rds.Options{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	})
	return &sdkV2ModifyDBInstance{
		client: awsClient,
	}
}

func (m *sdkV2ModifyDBInstance) ModifyDBInstance(ctx context.Context, params *rds.ModifyDBInstanceInput, optFns ...func(*rds.Options)) (*rds.ModifyDBInstanceOutput, error) {
	return m.client.ModifyDBInstance(ctx, params, optFns...)
}
