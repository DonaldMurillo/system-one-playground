// Command sosverify reads the linked SysOneScript release marker from a Go
// executable without executing it, including foreign-platform release builds.
package main

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
)

const markerSymbol = "github.com/DonaldMurillo/system-one-playground/sos.ReleaseMarker"

type executable struct {
	order  binary.ByteOrder
	addr   uint64
	reader func(uint64, uint64) ([]byte, error)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sosverify EXECUTABLE EXPECTED_MARKER")
		os.Exit(2)
	}
	exe, err := openExecutable(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	header, err := exe.reader(exe.addr, 16)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dataAddr := exe.order.Uint64(header[:8])
	length := exe.order.Uint64(header[8:])
	if length > 4096 {
		fmt.Fprintln(os.Stderr, "release marker has unreasonable length")
		os.Exit(1)
	}
	data, err := exe.reader(dataAddr, length)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if string(data) != os.Args[2] {
		fmt.Fprintf(os.Stderr, "linked release marker %q does not match %q\n", data, os.Args[2])
		os.Exit(1)
	}
}

func openExecutable(path string) (*executable, error) {
	if f, err := elf.Open(path); err == nil {
		symbols, err := f.Symbols()
		if err != nil {
			return nil, err
		}
		var addr uint64
		for _, symbol := range symbols {
			if symbol.Name == markerSymbol {
				addr = symbol.Value
				break
			}
		}
		return &executable{order: f.ByteOrder, addr: addr, reader: func(address, size uint64) ([]byte, error) {
			for _, section := range f.Sections {
				if address >= section.Addr && address+size <= section.Addr+section.Size {
					return readAt(section, int64(address-section.Addr), size)
				}
			}
			return nil, fmt.Errorf("ELF address %#x is not mapped", address)
		}}, requireSymbol(addr)
	}
	if f, err := macho.Open(path); err == nil {
		var addr uint64
		if f.Symtab != nil {
			for _, symbol := range f.Symtab.Syms {
				if strings.TrimPrefix(symbol.Name, "_") == markerSymbol {
					addr = symbol.Value
					break
				}
			}
		}
		return &executable{order: f.ByteOrder, addr: addr, reader: func(address, size uint64) ([]byte, error) {
			for _, section := range f.Sections {
				if address >= section.Addr && address+size <= section.Addr+section.Size {
					return readAt(section, int64(address-section.Addr), size)
				}
			}
			return nil, fmt.Errorf("Mach-O address %#x is not mapped", address)
		}}, requireSymbol(addr)
	}
	if f, err := pe.Open(path); err == nil {
		imageBase := uint64(0)
		switch header := f.OptionalHeader.(type) {
		case *pe.OptionalHeader64:
			imageBase = header.ImageBase
		default:
			return nil, fmt.Errorf("unsupported 32-bit PE executable")
		}
		var addr uint64
		for _, symbol := range f.Symbols {
			if symbol.Name == markerSymbol && symbol.SectionNumber > 0 {
				section := f.Sections[symbol.SectionNumber-1]
				addr = imageBase + uint64(section.VirtualAddress) + uint64(symbol.Value)
				break
			}
		}
		return &executable{order: binary.LittleEndian, addr: addr, reader: func(address, size uint64) ([]byte, error) {
			for _, section := range f.Sections {
				start := imageBase + uint64(section.VirtualAddress)
				if address >= start && address+size <= start+uint64(section.Size) {
					return readAt(section, int64(address-start), size)
				}
			}
			return nil, fmt.Errorf("PE address %#x is not mapped", address)
		}}, requireSymbol(addr)
	}
	return nil, fmt.Errorf("unsupported executable format: %s", path)
}

func requireSymbol(address uint64) error {
	if address == 0 {
		return fmt.Errorf("release marker symbol not found")
	}
	return nil
}

func readAt(reader io.ReaderAt, offset int64, size uint64) ([]byte, error) {
	data := make([]byte, size)
	_, err := reader.ReadAt(data, offset)
	return data, err
}
