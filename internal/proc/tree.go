package proc

// treeController abstracts the OS container that owns a process tree. Windows
// uses a Job Object (with a thread walk for suspend/resume); Unix uses a process
// group driven by SIGSTOP/SIGCONT/SIGKILL.
type treeController interface {
	// Pause suspends every process in the tree.
	Pause() error
	// Resume undoes Pause.
	Resume() error
	// Kill terminates the whole tree.
	Kill() error
	// Close releases the container without killing its members.
	Close() error
}
