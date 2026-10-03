//go:build windows

package workspacesync

import (
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// watch reports changed root-relative paths until stop is called. overflow
// means notifications were lost and the caller must rescan. Windows records
// changes only once the first ReadDirectoryChanges call is issued (and
// buffers them between calls from then on), so that call is made before
// watch returns: every change after watch returns is reported.
func watch(root string, report func(paths []string, overflow bool)) (func(), error) {
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	// Both live on the heap: the kernel writes to them asynchronously and
	// goroutine stacks may move.
	buffer := make([]byte, 256<<10)
	overlapped := new(windows.Overlapped)
	mask := uint32(windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME | windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE)
	issue := func() error {
		*overlapped = windows.Overlapped{HEvent: event}
		windows.ResetEvent(event)
		return windows.ReadDirectoryChanges(handle, &buffer[0], uint32(len(buffer)), true, mask, nil, overlapped, 0)
	}
	if err := issue(); err != nil {
		windows.CloseHandle(event)
		windows.CloseHandle(handle)
		return nil, err
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(finished)
		defer windows.CloseHandle(event)
		defer windows.CloseHandle(handle)
		for {
			for {
				result, _ := windows.WaitForSingleObject(event, 300)
				if result == windows.WAIT_OBJECT_0 {
					break
				}
				select {
				case <-done:
					windows.CancelIoEx(handle, overlapped)
					var ignored uint32
					windows.GetOverlappedResult(handle, overlapped, &ignored, true)
					return
				default:
				}
			}
			var size uint32
			failed := windows.GetOverlappedResult(handle, overlapped, &size, false) != nil
			var paths []string
			for offset := uint32(0); !failed && offset < size; {
				info := (*windows.FileNotifyInformation)(unsafe.Pointer(&buffer[offset]))
				changed := windows.UTF16ToString(unsafe.Slice(&info.FileName, info.FileNameLength/2))
				paths = append(paths, filepath.ToSlash(changed))
				if info.NextEntryOffset == 0 {
					break
				}
				offset += info.NextEntryOffset
			}
			select {
			case <-done:
				return
			default:
			}
			// Re-arm before reporting: changes made while the caller handles
			// this batch are buffered by the system.
			if err := issue(); err != nil {
				report(nil, true)
				return
			}
			// A failed read or an empty result means the kernel buffer
			// overflowed and changes were lost.
			report(paths, failed || size == 0)
		}
	}()
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}, nil
}
