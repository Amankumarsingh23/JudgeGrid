package execution

import "fmt"

// Fixture returns an approved, bounded local test workload. This is not a public execution endpoint.
func Fixture(name string) (Request, error) {
	r := Request{Language: "python3", CompileLimits: DefaultCompileLimits(), RunLimits: DefaultRunLimits()}
	sources := map[string]string{
		"python-hello":     "a,b=map(int,input().split());print(a+b)",
		"python-sleep":     "import time;time.sleep(20)",
		"python-memory":    "x=bytearray(256*1024*1024);print(len(x))",
		"python-stdout":    "import os\nwhile True: os.write(1,b'x'*4096)",
		"python-stderr":    "import os\nwhile True: os.write(2,b'x'*4096)",
		"python-crash":     "raise RuntimeError('controlled fixture crash')",
		"python-network":   "import socket\ntry:\n socket.create_connection(('1.1.1.1',80),0.2);print('UNEXPECTED_NETWORK')\nexcept OSError as e: print('NETWORK_DENIED',e.errno)",
		"python-files":     "import os\nfor p in ['/var/run/docker.sock','/judgegrid-host-only-secret','/etc/shadow']:\n try: open(p,'rb').read(1);print('UNEXPECTED_READ',p)\n except OSError: print('READ_DENIED',p)\nfor p in ['/etc/judgegrid-write','/work/judgegrid-write']:\n try: open(p,'w').write('x');print('UNEXPECTED_WRITE',p)\n except OSError: print('WRITE_DENIED',p)\nprint('SECRET_ENV',any(k in os.environ for k in ['DATABASE_URL','REDIS_PASSWORD','JUDGEGRID_HOST_SECRET']))",
		"python-processes": "import subprocess\nchildren=[]\ntry:\n for i in range(30): children.append(subprocess.Popen(['/bin/sleep','2']))\n print('UNEXPECTED_PIDS')\nexcept OSError as e: print('PID_LIMIT_ENFORCED' if e.errno==11 else 'UNEXPECTED_PID_ERROR',len(children),e.errno)\nfinally:\n for c in children: c.kill()\n for c in children: c.wait()",
		"python-policy":    "import os,ctypes\ns=open('/proc/self/status').read();print(s)\nprint('UID',os.getuid());print('CPU_MAX',open('/sys/fs/cgroup/cpu.max').read().strip());print('MEMORY_MAX',open('/sys/fs/cgroup/memory.max').read().strip());print('PIDS_MAX',open('/sys/fs/cgroup/pids.max').read().strip())\nlibc=ctypes.CDLL(None,use_errno=True);rc=libc.unshare(0x10000000);print('NAMESPACE_CHANGE_DENIED' if rc==-1 and ctypes.get_errno()==1 else 'UNEXPECTED_UNSHARE',rc,ctypes.get_errno())",
	}
	switch name {
	case "cpp-hello":
		r.Language = "cpp17"
		r.Source = []byte("#include <iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<a+b<<'\\n';}")
	case "cpp-syntax":
		r.Language = "cpp17"
		r.Source = []byte("int main( { syntax error }")
	case "cpp-loop":
		r.Language = "cpp17"
		r.Source = []byte("int main(){for(;;){}}")
	case "cpp-crash":
		r.Language = "cpp17"
		r.Source = []byte("#include <csignal>\nint main(){raise(SIGSEGV);}")
	default:
		source, ok := sources[name]
		if !ok {
			return Request{}, fmt.Errorf("unknown approved fixture %q", name)
		}
		r.Source = []byte(source)
	}
	r.Stdin = []byte("2 3\n")
	return r, nil
}
