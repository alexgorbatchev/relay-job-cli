package job

import (
	"context"
	"errors"
	"io"
	"os"
)

func (c *Coordinator) copyLogs(ctx context.Context, r Record) error {
	pending := false
	for _, stream := range []string{"stdout", "stderr"} {
		path := c.Store.Path(r.ID, stream)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		stat, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if r.Worker == "local" {
			source, err := os.Open(c.Local.Store.Path(r.ID, stream))
			if os.IsNotExist(err) {
				f.Close()
				continue
			}
			if err != nil {
				f.Close()
				return err
			}
			_, err = source.Seek(stat.Size(), io.SeekStart)
			if err == nil {
				var n int64
				n, err = io.Copy(f, io.LimitReader(source, maxLogBatch))
				if n == maxLogBatch {
					pending = true
				}
			}
			source.Close()
			if err == nil {
				err = f.Sync()
			}
			f.Close()
			if err != nil {
				return err
			}
		} else {
			w := c.worker(r.Worker)
			rpc, err := w.Connect(ctx)
			if err != nil {
				f.Close()
				return err
			}
			// Drain every chunk before publishing terminal status.
			offset := stat.Size()
			var copied int64
			for copied < maxLogBatch {
				n, readErr := rpc.Log(ctx, r.ID, stream, offset, f)
				if readErr != nil {
					f.Close()
					return readErr
				}
				copied += n
				offset += n
				if n < 1<<20 {
					break
				}
			}
			if copied == maxLogBatch {
				pending = true
			}
			if copied > 0 {
				if err = f.Sync(); err != nil {
					f.Close()
					return err
				}
			}
			if err = f.Close(); err != nil {
				return err
			}
		}
	}
	if pending {
		return errLogsPending
	}
	return nil
}

const maxLogBatch = 8 << 20

var errLogsPending = errors.New("job output is still being collected")
