// Copyright 2018 Vector Creations Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"database/sql"
	"github.com/matrix-org/dendrite/common"
	"context"
	"github.com/matrix-org/dendrite/encryptoapi/types"
	"strings"
)

// Database represents a presence database.
type Database struct {
	db            *sql.DB
	keyStatements keyStatements
	alStatements  alStatements
}

// NewDatabase creates a new presence database
func NewDatabase(dataSourceName string) (*Database, error) {
	var db *sql.DB
	var err error
	if db, err = sql.Open("postgres", dataSourceName); err != nil {
		return nil, err
	}
	keyStatement := keyStatements{}
	alStatement := alStatements{}
	if err = keyStatement.prepare(db); err != nil {
		return nil, err
	}
	if err = alStatement.prepare(db); err != nil {
		return nil, err
	}
	return &Database{db: db, keyStatements: keyStatement, alStatements: alStatement}, nil
}

// insert device key
func (d *Database) InsertKey(
	ctx context.Context,
	deviceID, userID, keyID, keyTyp, keyInfo, al, sig string,
) (err error) {
	err = common.WithTransaction(d.db, func(txn *sql.Tx) error {
		return d.keyStatements.insertKey(ctx, txn, deviceID, userID, keyID, keyTyp, keyInfo, al, sig)
	})
	return
}

// for key upload response usage a map from key algorithm to sum to counterpart
func (d *Database) SelectOneTimeKeyCount(
	ctx context.Context,
	deviceID, userID string,
) (m map[string]int, err error) {
	m = make(map[string]int)
	err = common.WithTransaction(d.db, func(txn *sql.Tx) error {
		elems, err := d.keyStatements.selectKey(ctx, txn, deviceID, userID)
		for _, val := range elems {
			if _, ok := m[val.Key_algorithm]; !ok {
				m[val.Key_algorithm] = 0
			}
			if val.Key_type == "one_time_key" {
				m[val.Key_algorithm] += 1
			}
		}
		return err
	})
	return
}

// query keys in a range of devices
func (d *Database) QueryInRange(
	ctx context.Context,
	userID string,
	arr []string,
) (res []types.KeyHolder, err error) {
	res, err = d.keyStatements.selectInKeys(ctx, userID, arr)
	return
}

// persist algorithms
func (d *Database) InsertAl(
	ctx context.Context, uid, device string, al []string,
) (err error) {
	err = common.WithTransaction(d.db, func(txn *sql.Tx) (err error) {
		d.alStatements.insertAl(ctx, txn, uid, device, strings.Join(al, ","))
		return
	})
	return
}

// select algorithms
func (d *Database) SelectAl(
	ctx context.Context, uid, device string,
) (res []string, err error) {
	err = common.WithTransaction(d.db, func(txn *sql.Tx) (err error) {
		holder, err := d.alStatements.selectAl(ctx, txn, uid, device)
		res = strings.Split(holder.Supported_algorithm, ",")
		return
	})
	return
}

// claim for one time key one for once
func (d *Database) SelectOneTimeKeySingle(
	ctx context.Context,
	userID, deviceID, algorithm string,
) (holder types.KeyHolder, err error) {
	holder, err = d.keyStatements.selectSingleKey(ctx, userID, deviceID, algorithm)
	return
}
