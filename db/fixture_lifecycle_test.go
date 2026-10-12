package db

import "testing"

func TestFixtureRowCleanupBeforeStoreClose(t *testing.T) {
	for _, contract := range []bool{false, true} {
		name := "ordinary"
		if contract {
			name = "contract"
		}
		var d *DB
		t.Run(name, func(t *testing.T) {
			if contract {
				d = contractTestDB(t)
			} else {
				d, _ = openTestDB(t)
			}
			task, err := d.CreateTask("fixture cleanup order", "rows before close", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := d.DeleteTask(task.ID); err != nil {
					t.Error("fixture row deletion:", err)
					return
				}
				if got, err := d.GetTask(task.ID); err != nil || got != nil {
					t.Errorf("fixture row survived: %+v %v", got, err)
				}
			})
		})
		if d != nil && d.Ping() == nil {
			t.Errorf("%s fixture left its store open", name)
		}
	}
}

func TestContractFixtureJoinsPeerBeforeGuardedReset(t *testing.T) {
	var primary, peer *DB
	t.Run("contract", func(t *testing.T) {
		primary = contractTestDB(t)
		var err error
		peer, err = reopenTestDB(t, primary)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := peer.Close(); err != nil {
				t.Error("peer close:", err)
			}
		})
		task, err := primary.CreateTask("fixture final peer write", "join before reset", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			<-stop
			name := "peer joined"
			_, err := peer.UpdateTask(task.ID, TaskPatch{Name: &name})
			done <- err
		}()
		t.Cleanup(func() {
			close(stop)
			if err := <-done; err != nil {
				t.Error("peer final write:", err)
			}
			if got, err := primary.GetTask(task.ID); err != nil || got == nil || got.Name != "peer joined" {
				t.Errorf("peer final write missing: %+v %v", got, err)
			}
		})
	})
	for _, d := range []*DB{primary, peer} {
		if d != nil && d.Ping() == nil {
			t.Error("contract kept a database session open after reset")
		}
	}
}
