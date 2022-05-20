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

package controllers

import (
	cryptorand "crypto/rand"
	"math/big"
	"math/rand"
)

const (
	digits   = "0123456789"
	specials = "~=+%^*()[]{}!#$?|"
	all      = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" + digits + specials
)

func generateUsername(engine string) string {
	if engine == "postgres" || engine == "aurora-postgresql" {
		return "postgres"
	} else {
		return "admin"
	}
}

func generateDBName(engine string) *string {
	switch engine {
	case "postgres", "aurora-postgresql":
		dbName := "postgres"
		return &dbName
	case "mysql", "mariadb", "aurora", "aurora-mysql":
		dbName := "mysql"
		return &dbName
	case "oracle-se2", "oracle-se2-cdb", "oracle-ee", "oracle-ee-cdb", "custom-oracle-ee":
		dbName := "orcl"
		return &dbName
	default:
		return nil
	}
}

func generatePassword() string {
	length := 12
	buf := make([]byte, length)
	buf[0] = digits[getRandInt(len(digits))]
	buf[1] = specials[getRandInt(len(specials))]
	for i := 2; i < length; i++ {
		buf[i] = all[getRandInt(len(all))]
	}
	rand.Shuffle(len(buf), func(i, j int) {
		buf[i], buf[j] = buf[j], buf[i]
	})
	return string(buf) // E.g. "3i[g0|)z"
}

func getRandInt(s int) int64 {
	result, _ := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(s)))
	return result.Int64()
}
