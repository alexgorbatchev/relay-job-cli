package job

const maxDiagnosticBytes = 4096
const diagnosticOmitted = "[earlier diagnostics omitted]\n"

// diagnosticBuffer retains the newest diagnostics while fully draining a child.
// The owning exec.Cmd joins its writer before the transport reads or resets it.
type diagnosticBuffer struct {
	data      []byte
	truncated bool
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= maxDiagnosticBytes {
		b.truncated = b.truncated || len(b.data)+n > maxDiagnosticBytes
		b.data = append(b.data[:0], p[n-maxDiagnosticBytes:]...)
		return n, nil
	}
	if over := len(b.data) + n - maxDiagnosticBytes; over > 0 {
		copy(b.data, b.data[over:])
		b.data = b.data[:len(b.data)-over]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *diagnosticBuffer) String() string {
	if b.truncated {
		return diagnosticOmitted + string(b.data)
	}
	return string(b.data)
}
func (b *diagnosticBuffer) Reset() { b.data = b.data[:0]; b.truncated = false }
