"""Compatibility entry point for candidate verification."""
import argparse
from pathlib import Path
from verify_candidate import verify

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("archive", type=Path)
    verify(parser.parse_args().archive)
