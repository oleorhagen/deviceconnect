// Copyright 2021 Northern.tech AS
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//        http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

package mongo

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	mopts "go.mongodb.org/mongo-driver/mongo/options"

	"github.com/mendersoftware/deviceconnect/model"
	"github.com/mendersoftware/deviceconnect/store"
	"github.com/mendersoftware/go-lib-micro/identity"
	mstore "github.com/mendersoftware/go-lib-micro/store"
)

type mockClock struct{}

var (
	mockTime = time.Date(2018, 01, 12, 22, 51, 48, 324000000, time.UTC)
)

func (m mockClock) Now() time.Time {
	return mockTime
}

func TestPing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TestPing in short mode.")
	}
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*10)
	defer cancel()

	ds := NewDataStoreWithClient(db.Client(), time.Minute)
	err := ds.Ping(ctx)
	assert.NoError(t, err)
}

func TestProvisionTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TestPing in short mode.")
	}
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*10)
	defer cancel()

	ds := DataStoreMongo{client: db.Client()}
	defer ds.DropDatabase()
	err := ds.ProvisionTenant(ctx, "1234")
	assert.NoError(t, err)
}

func TestProvisionAndDeleteDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TestPing in short mode.")
	}
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*10)
	defer cancel()

	const (
		tenantID = "1234"
		deviceID = "abcd"
	)

	previousClock := clock
	defer func() {
		clock = previousClock
	}()

	clock = mockClock{}

	ds := DataStoreMongo{client: db.Client()}
	defer ds.DropDatabase()
	err := ds.ProvisionDevice(ctx, tenantID, deviceID)
	assert.NoError(t, err)

	device, err := ds.GetDevice(ctx, tenantID, deviceID)
	assert.NoError(t, err)
	assert.Equal(t, deviceID, device.ID)
	assert.Equal(t, mockTime, device.CreatedTs)
	assert.Equal(t, mockTime, device.UpdatedTs)

	err = ds.DeleteDevice(ctx, tenantID, deviceID)
	assert.NoError(t, err)

	device, err = ds.GetDevice(ctx, tenantID, deviceID)
	assert.NoError(t, err)
	assert.Nil(t, device)
}

func TestUpsertDeviceStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TestPing in short mode.")
	}
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*10)
	defer cancel()

	const (
		tenantID = "1234"
		deviceID = "abcd"
	)

	ds := DataStoreMongo{client: db.Client()}
	defer ds.DropDatabase()
	err := ds.ProvisionDevice(ctx, tenantID, deviceID)
	assert.NoError(t, err)

	device, err := ds.GetDevice(ctx, tenantID, deviceID)
	assert.NoError(t, err)
	assert.Equal(t, model.DeviceStatusUnknown, device.Status)

	previousClock := clock
	defer func() {
		clock = previousClock
	}()

	clock = mockClock{}

	err = ds.UpsertDeviceStatus(ctx, tenantID, deviceID, model.DeviceStatusConnected)
	assert.NoError(t, err)

	device, err = ds.GetDevice(ctx, tenantID, deviceID)
	assert.NoError(t, err)
	assert.Equal(t, model.DeviceStatusConnected, device.Status)
	assert.NotEqual(t, mockTime, device.CreatedTs)
	assert.Equal(t, mockTime, device.UpdatedTs)

	const anotherDeviceID = "efgh"
	err = ds.UpsertDeviceStatus(ctx, tenantID, anotherDeviceID, model.DeviceStatusConnected)
	assert.NoError(t, err)

	device, err = ds.GetDevice(ctx, tenantID, anotherDeviceID)
	assert.NoError(t, err)
	assert.Equal(t, model.DeviceStatusConnected, device.Status)

	err = ds.UpsertDeviceStatus(ctx, tenantID, anotherDeviceID, model.DeviceStatusDisconnected)
	assert.NoError(t, err)

	device, err = ds.GetDevice(ctx, tenantID, anotherDeviceID)
	assert.NoError(t, err)
	assert.Equal(t, model.DeviceStatusDisconnected, device.Status)
}

type brokenReader struct{}

func (r brokenReader) Read(b []byte) (int, error) {
	return 0, errors.New("broken reader")
}

func TestAllocateSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TestAllocateSession in short mode.")
	}
	testCases := []struct {
		Name string

		CTX     context.Context
		Session *model.Session

		Erre error
	}{{
		Name: "ok",

		CTX: context.Background(),
		Session: &model.Session{
			ID:       "0ff7cda3-a398-43b0-9776-6622cb6aa110",
			UserID:   "9f56b9c3-d510-4107-9686-8a1c4969e02d",
			DeviceID: "818c6ec3-051e-42ce-be79-7f75bc2b2da9",
			TenantID: "123456789012345678901234",
			StartTS:  time.Now(),
		},
	}, {
		Name: "error, invalid session object",

		CTX: context.Background(),
		Session: &model.Session{
			ID:       "0ff7cda3-a398-43b0-9776-6622cb6aa111",
			UserID:   "9f56b9c3-d510-4107-9686-8a1c4969e02d",
			DeviceID: "818c6ec3-051e-42ce-be79-7f75bc2b2da9",
			TenantID: "123456789012345678901234",
		},
		Erre: errors.New("store: cannot allocate invalid Session: " +
			"start_ts: cannot be blank."),
	}, {
		Name: "error, context canceled",

		CTX: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}(),
		Session: &model.Session{
			ID:       "0ff7cda3-a398-43b0-9776-6622cb6aa112",
			UserID:   "9f56b9c3-d510-4107-9686-8a1c4969e02d",
			DeviceID: "818c6ec3-051e-42ce-be79-7f75bc2b2da9",
			TenantID: "123456789012345678901234",
			StartTS:  time.Now(),
		},

		Erre: errors.New(context.Canceled.Error() + `$`),
	}}

	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.Name, func(t *testing.T) {
			ds := DataStoreMongo{client: db.Client()}
			defer ds.DropDatabase()

			err := ds.AllocateSession(tc.CTX, tc.Session)
			if tc.Erre != nil {
				if assert.Error(t, err) {
					assert.Regexp(t, tc.Erre.Error(), err.Error())
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDeleteSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TestDeleteSession in short mode.")
	}
	testCases := []struct {
		Name string

		CTX       context.Context
		SessionID string

		Session *model.Session

		Erre error
	}{{
		Name: "ok",

		CTX: identity.WithContext(
			context.Background(),
			&identity.Identity{
				Tenant: "000000000000000000000000",
			},
		),
		SessionID: "00000000-0000-0000-0000-000000000000",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			TenantID: "000000000000000000000000",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
	}, {
		Name: "ok, no tenant",

		CTX:       context.Background(),
		SessionID: "00000000-0000-0000-0000-000000000000",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
	}, {
		Name: "error, context canceled",

		CTX: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}(),
		SessionID: "00000000-0000-0000-0000-000000000000",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
		Erre: errors.New(context.Canceled.Error() + "$"),
	}, {
		Name: "error, session not found",

		CTX:       context.Background(),
		SessionID: "00000000-0000-0000-0000-000012345678",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
		Erre: errors.New("^" + store.ErrSessionNotFound.Error() + "$"),
	}}
	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.Name, func(t *testing.T) {
			ds := DataStoreMongo{client: db.Client()}
			defer ds.DropDatabase()

			database := db.Client().Database(mstore.DbNameForTenant(
				tc.Session.TenantID, DbName,
			))
			collSess := database.Collection(SessionsCollectionName)
			_, err := collSess.InsertOne(nil, tc.Session)
			if err != nil {
				panic(errors.Wrap(err,
					"[TEST ERR] Failed to prepare test case",
				))
			}
			sess, err := ds.DeleteSession(tc.CTX, tc.SessionID)
			if tc.Erre != nil {
				if assert.Error(t, err) {
					assert.Regexp(t, tc.Erre.Error(), err.Error())
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.Session, sess)
			}
		})
	}
}

func TestGetSession(t *testing.T) {
	testCases := []struct {
		Name string

		CTX       context.Context
		SessionID string

		Session *model.Session

		Erre error
	}{{
		Name: "ok",

		CTX: identity.WithContext(
			context.Background(),
			&identity.Identity{
				Tenant: "000000000000000000000000",
			},
		),
		SessionID: "00000000-0000-0000-0000-000000000000",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			TenantID: "000000000000000000000000",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
	}, {
		Name: "ok, no tenant",

		CTX:       context.Background(),
		SessionID: "00000000-0000-0000-0000-000000000000",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
	}, {
		Name: "error, context canceled",

		CTX: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}(),
		SessionID: "00000000-0000-0000-0000-000000000000",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
		Erre: errors.New(context.Canceled.Error() + "$"),
	}, {
		Name: "error, session not found",

		CTX:       context.Background(),
		SessionID: "00000000-0000-0000-0000-000012345678",
		Session: &model.Session{
			ID:       "00000000-0000-0000-0000-000000000000",
			UserID:   "00000000-0000-0000-0000-000000000001",
			DeviceID: "00000000-0000-0000-0000-000000000002",
			StartTS:  time.Now().UTC().Round(time.Second),
		},
		Erre: errors.New("^" + store.ErrSessionNotFound.Error() + "$"),
	}}
	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.Name, func(t *testing.T) {
			ds := &DataStoreMongo{client: db.Client()}
			defer ds.DropDatabase()

			database := db.Client().Database(mstore.DbNameForTenant(
				tc.Session.TenantID, DbName,
			))
			collSess := database.Collection(SessionsCollectionName)
			_, err := collSess.InsertOne(nil, tc.Session)
			if err != nil {
				panic(errors.Wrap(err,
					"[TEST ERR] Failed to prepare test case",
				))
			}

			sess, err := ds.GetSession(tc.CTX, tc.SessionID)
			if tc.Erre != nil {
				if assert.Error(t, err) {
					assert.Regexp(t, tc.Erre.Error(), err.Error())
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.Session, sess)
			}
		})
	}
}

type sessionWriterTest struct {
	c chan []byte
}

func (s *sessionWriterTest) Write(d []byte) (int, error) {
	s.c <- d
	return len(d), nil
}

func TestGetSessionRecording(t *testing.T) {
	testCases := []struct {
		Name string

		Ctx           context.Context
		SessionID     string
		RecordingData string

		Error error
	}{
		{
			Name: "ok",

			Ctx: identity.WithContext(
				context.Background(),
				&identity.Identity{
					Tenant: "000000000000000000000000",
				},
			),
			SessionID:     "00000000-0000-0000-0000-000000000000",
			RecordingData: "H4sIAAAAAAAA/5TNscrDMAwE4P2H/wm83NjJtSTbsdyta8eMWZNCKWlomvenONlMKOQGG8RxH5EmjxYAWweArAAimdn6iHF49cOMOuZ0Nd1oOtGL19F0t/+/7URc1uZpWrYil0kHqFBoIsCBJQLEFFONwmcSm+Q4qknDLqrJ+4I2ygVl5RplytxYdYfRMhB30DWu+tqVWxvbm73a4PD8TPflMb/7M/1EvwAAAP//AQAA//8CyiAFpQEAAA==",
		},
	}

	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.Name, func(t *testing.T) {
			ds := &DataStoreMongo{client: db.Client()}
			defer ds.DropDatabase()

			database := db.Client().Database(mstore.DbNameForTenant(
				"000000000000000000000000", DbName,
			))
			collSess := database.Collection(RecordingsCollectionName)
			d, err := base64.StdEncoding.DecodeString(tc.RecordingData)
			assert.NoError(t, err)

			_, err = collSess.InsertOne(nil, &model.Recording{
				ID:        uuid.New(),
				SessionID: tc.SessionID,
				Recording: d,
				CreatedTs: time.Now().UTC(),
				ExpireTs:  time.Now().UTC(),
			})
			assert.NoError(t, err)

			readRecordingChannel := make(chan []byte, 1)
			sessionWriter := &sessionWriterTest{c: readRecordingChannel}
			go ds.GetSessionRecording(tc.Ctx, tc.SessionID, sessionWriter)

			if tc.Error != nil {
			} else {
				assert.NoError(t, err)
				select {
				case recording := <-sessionWriter.c:
					var buffer bytes.Buffer

					_, e := buffer.Write(d)
					assert.NoError(t, e)
					gzipReader, e := gzip.NewReader(&buffer)
					assert.NoError(t, e)

					output := make([]byte, recordingReadBufferSize)
					n, e := gzipReader.Read(output)
					assert.NoError(t, e)
					gzipReader.Close()
					assert.Equal(t, recording, output[:n])
				case <-time.After(time.Second):
					t.Fatal("cannot read the recording data.")
					t.Fail()
				}
			}
		})
	}
}

func TestSetSessionRecording(t *testing.T) {
	testCases := []struct {
		Name string

		Ctx           context.Context
		SessionID     string
		RecordingData []byte
		Expiration    time.Duration
		Expire        bool

		Error error
	}{
		{
			Name: "ok",

			Ctx: identity.WithContext(
				context.Background(),
				&identity.Identity{
					Tenant: "000000000000000000000001",
				},
			),
			SessionID:     "00000000-0000-0000-0000-000000000001",
			RecordingData: []byte("ls -al\r\n"),
			Expiration:    time.Hour,
		},
		{
			Name: "ok expired",

			Ctx: identity.WithContext(
				context.Background(),
				&identity.Identity{
					Tenant: "000000000000000000000002",
				},
			),
			SessionID:     "00000000-0000-0000-0000-000000000002",
			RecordingData: []byte("ls -al\r\n"),
			Expiration:    time.Second,
			Expire:        true,
		},
	}

	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.Name, func(t *testing.T) {
			ds := &DataStoreMongo{
				client:          db.Client(),
				recordingExpire: tc.Expiration,
			}
			defer ds.DropDatabase()

			database := db.Client().Database(mstore.DbNameForTenant(
				"000000000000000000000001", DbName,
			))
			collSess := database.Collection(RecordingsCollectionName)

			indexModels := []mongo.IndexModel{
				{
					// Index for expiring old events
					Keys: bson.D{{Key: "expire_ts", Value: 1}},
					Options: mopts.Index().
						SetBackground(true).
						SetExpireAfterSeconds(0).
						SetName(IndexNameLogsExpire),
				},
			}
			idxView := collSess.Indexes()

			_, err := idxView.CreateMany(tc.Ctx, indexModels)

			ds.InsertSessionRecording(tc.Ctx, tc.SessionID, tc.RecordingData)

			if tc.Expire {
				time.Sleep(4 * tc.Expiration)
			}

			var r model.Recording
			res := collSess.FindOne(nil,
				bson.M{
					dbFieldSessionID: tc.SessionID,
				},
			)
			assert.NotNil(t, res)

			err = res.Decode(&r)
			if tc.Expire {
				assert.EqualError(t, err, "mongo: no documents in result")
			} else {
				assert.Equal(t, tc.RecordingData, r.Recording)
			}
		})
	}
}
