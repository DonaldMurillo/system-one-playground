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
)

const markerSymbol = "github.com/DonaldMurillo/system-one-playground/sos.ReleaseMarker"

type executable struct {
	order  binary.ByteOrder
	header []byte
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
	header := exe.header
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
		var matches []elf.Symbol
		for _, symbol := range symbols {
			if symbol.Name == markerSymbol {
				matches = append(matches, symbol)
			}
		}
		if len(matches) != 1 || elf.ST_TYPE(matches[0].Info) != elf.STT_OBJECT || elf.ST_BIND(matches[0].Info) != elf.STB_GLOBAL || matches[0].Section == elf.SHN_UNDEF || int(matches[0].Section) >= len(f.Sections) {
			return nil, fmt.Errorf("release marker must have exactly one defined ELF object symbol")
		}
		markerSection := f.Sections[matches[0].Section]
		if !elfLoadable(f, markerSection) || markerSection.Flags&elf.SHF_WRITE == 0 {
			return nil, fmt.Errorf("ELF release marker symbol is not in an allocated data section")
		}
		header, err := readAddress(markerSection, markerSection.Addr, markerSection.Size, matches[0].Value, 16)
		if err != nil {
			return nil, fmt.Errorf("ELF release marker header: %w", err)
		}
		return &executable{order: f.ByteOrder, header: header, reader: func(address, size uint64) ([]byte, error) {
			for _, section := range f.Sections {
				if elfLoadable(f, section) && contains(section.Addr, section.Size, address, size) {
					return readAddress(section, section.Addr, section.Size, address, size)
				}
			}
			return nil, fmt.Errorf("ELF address %#x is not mapped", address)
		}}, nil
	}
	if f, err := macho.Open(path); err == nil {
		var matches []macho.Symbol
		if f.Symtab != nil {
			for _, symbol := range f.Symtab.Syms {
				if symbol.Name == markerSymbol {
					matches = append(matches, symbol)
				}
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("release marker must have exactly one Mach-O symbol")
		}
		// Go emits package data symbols as private externs in Mach-O files, so
		// N_EXT cannot be required here. Exact-name uniqueness plus the linked,
		// file-backed section and marker value checks establish the identity.
		if matches[0].Type&0x0e != 0x0e || matches[0].Sect == 0 || int(matches[0].Sect) > len(f.Sections) {
			return nil, fmt.Errorf("release marker must be a defined Mach-O section symbol (type %#x, section %d)", matches[0].Type, matches[0].Sect)
		}
		markerSection := f.Sections[matches[0].Sect-1]
		if !machoLoadable(f, markerSection) || markerSection.Seg != "__DATA" {
			return nil, fmt.Errorf("Mach-O release marker symbol is not in a loadable data section")
		}
		header, err := readAddress(markerSection, markerSection.Addr, markerSection.Size, matches[0].Value, 16)
		if err != nil {
			return nil, fmt.Errorf("Mach-O release marker header: %w", err)
		}
		return &executable{order: f.ByteOrder, header: header, reader: func(address, size uint64) ([]byte, error) {
			for _, section := range f.Sections {
				if machoLoadable(f, section) && contains(section.Addr, section.Size, address, size) {
					return readAddress(section, section.Addr, section.Size, address, size)
				}
			}
			return nil, fmt.Errorf("Mach-O address %#x is not mapped", address)
		}}, nil
	}
	if f, err := pe.Open(path); err == nil {
		imageBase := uint64(0)
		switch header := f.OptionalHeader.(type) {
		case *pe.OptionalHeader64:
			imageBase = header.ImageBase
		default:
			return nil, fmt.Errorf("unsupported 32-bit PE executable")
		}
		var matches []*pe.Symbol
		for _, symbol := range f.Symbols {
			if symbol.Name == markerSymbol {
				matches = append(matches, symbol)
			}
		}
		if len(matches) != 1 || matches[0].SectionNumber <= 0 || int(matches[0].SectionNumber) > len(f.Sections) || matches[0].Type != 0 || matches[0].StorageClass != 2 {
			return nil, fmt.Errorf("release marker must have exactly one defined external PE data symbol")
		}
		section := f.Sections[matches[0].SectionNumber-1]
		if section.Characteristics&0x40000000 == 0 || section.Characteristics&0x80000000 == 0 || section.Characteristics&0x00000040 == 0 || section.Characteristics&0x22000020 != 0 {
			return nil, fmt.Errorf("PE release marker symbol is not in a readable loadable section")
		}
		sectionAddr, ok := checkedAdd(imageBase, uint64(section.VirtualAddress))
		if !ok {
			return nil, fmt.Errorf("PE section address overflows")
		}
		markerAddr, ok := checkedAdd(sectionAddr, uint64(matches[0].Value))
		if !ok {
			return nil, fmt.Errorf("PE release marker address overflows")
		}
		header, err := readAddress(section, sectionAddr, uint64(section.Size), markerAddr, 16)
		if err != nil {
			return nil, fmt.Errorf("PE release marker header: %w", err)
		}
		return &executable{order: binary.LittleEndian, header: header, reader: func(address, size uint64) ([]byte, error) {
			for _, section := range f.Sections {
				start, ok := checkedAdd(imageBase, uint64(section.VirtualAddress))
				if !ok {
					continue
				}
				if section.Characteristics&0x40000000 != 0 && section.Characteristics&0x22000020 == 0 && contains(start, uint64(section.Size), address, size) {
					return readAddress(section, start, uint64(section.Size), address, size)
				}
			}
			return nil, fmt.Errorf("PE address %#x is not mapped", address)
		}}, nil
	}
	return nil, fmt.Errorf("unsupported executable format: %s", path)
}

func contains(start, sectionSize, address, size uint64) bool {
	return address >= start && size <= sectionSize && address-start <= sectionSize-size
}

func checkedAdd(left, right uint64) (uint64, bool) {
	value := left + right
	return value, value >= left
}

func elfLoadable(file *elf.File, section *elf.Section) bool {
	if section.Type != elf.SHT_PROGBITS || section.Flags&elf.SHF_ALLOC == 0 || section.Flags&(elf.SHF_COMPRESSED|elf.SHF_EXECINSTR) != 0 || section.ReaderAt == nil {
		return false
	}
	for _, program := range file.Progs {
		if program.Type == elf.PT_LOAD && program.Flags&elf.PF_R != 0 && contains(program.Vaddr, program.Filesz, section.Addr, section.Size) && contains(program.Off, program.Filesz, section.Offset, section.Size) {
			return true
		}
	}
	return false
}

func machoLoadable(file *macho.File, section *macho.Section) bool {
	const instructionFlags = 0x80000400 // S_ATTR_PURE_INSTRUCTIONS | S_ATTR_SOME_INSTRUCTIONS
	if section.Flags&0xff != 0 || section.Flags&(0x02000000|instructionFlags) != 0 || section.ReaderAt == nil {
		return false
	}
	for _, load := range file.Loads {
		segment, ok := load.(*macho.Segment)
		if ok && segment.Prot&1 != 0 && contains(segment.Addr, segment.Filesz, section.Addr, section.Size) && contains(segment.Offset, segment.Filesz, uint64(section.Offset), section.Size) {
			return true
		}
	}
	return false
}

func readAddress(reader io.ReaderAt, start, sectionSize, address, size uint64) ([]byte, error) {
	if !contains(start, sectionSize, address, size) {
		return nil, fmt.Errorf("address %#x is outside its declared section", address)
	}
	return readAt(reader, int64(address-start), size)
}

func readAt(reader io.ReaderAt, offset int64, size uint64) ([]byte, error) {
	data := make([]byte, size)
	_, err := reader.ReadAt(data, offset)
	return data, err
}
