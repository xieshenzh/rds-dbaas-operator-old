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

package test

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	controllersrds "github.com/xieshenzh/rds-dbaas-operator/controllers/rds"
)

type mockDescribeDBInstancesPaginator struct {
}

func NewMockDescribeDBInstancesPaginator(accessKey, secretKey, region string) controllersrds.DescribeDBInstancesPaginatorAPI {
	return &mockDescribeDBInstancesPaginator{}
}

func (m *mockDescribeDBInstancesPaginator) HasMorePages() bool {
	return false
}

func (m *mockDescribeDBInstancesPaginator) NextPage(ctx context.Context, f ...func(option *rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	return nil, nil
}

type mockModifyDBInstance struct {
}

func NewModifyDBInstance(accessKey, secretKey, region string) controllersrds.ModifyDBInstanceAPI {
	return &mockModifyDBInstance{}
}

func (m *mockModifyDBInstance) ModifyDBInstance(ctx context.Context, params *rds.ModifyDBInstanceInput, optFns ...func(*rds.Options)) (*rds.ModifyDBInstanceOutput, error) {
	return nil, nil
}