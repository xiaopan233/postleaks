# Postleaks

![](assets/postleaks-50.png)

## Description

[Postman](https://www.postman.com/home) is an awesome platform to build and use APIs, used by millions developers.  
It proposes also public API assets built by them which can contains custom endpoints and data. Unfortunately, these items can leak sensitive data about private websites and companies.  
This script is aimed to search for these pieces of information in Postman public library.

## Installation

```bash
pip3 install postleaks
```

or 

```bash
pip3 install .
```

## Usage

```bash
❯ postleaks -h
usage: postleaks [-h] [-k KEYWORD] [-kf KEYWORD_FILE] [--extend-workspaces] [--strict] [--include INCLUDE] [--exclude EXCLUDE] [--raw] [--output OUTPUT]

Postleaks 🚀💧 Search for sensitive data in Postman public library.

options:
  -h, --help           show this help message and exit
  -k KEYWORD           Keyword (Domain, company, etc.)
  -kf KEYWORD_FILE     File containing keywords (one per line)
  --extend-workspaces  Extend search to Postman workspaces linked to found requests (warning: request consuming and risk of false positive)
  --strict             Only include results where keywords are in the URL (warning: could miss some results where the final URL is a variable)
  --include INCLUDE    URL should match this string
  --exclude EXCLUDE    URL should not match this string
  --raw                Display raw filtered results as JSON
  --output OUTPUT      Store all results in one JSON file (Default: results_<TIMESTAMP>.json)
  --debug              Display HTTP debug info (sent/received headers, request body/params, full responses
```

*All results are written to one JSON array file. The default filename is `results_<TIMESTAMP>.json`; use `--output path/to/results.json` to choose another path.*

```bash
postleaks -k example.com --output results.json
```

## Example

![](assets/example.png)

## Limitations

**~~02/2024: Postman.com is limiting the results to 300 items (page size of 100 / offset of 200).~~**  
**12/2024: Postman.com is limiting the results to 225 items (page size of 25 / offset of 200).**

## Notes

Secret detection is done with [whispers](https://github.com/adeptex/whispers). Rules are stored in `config.yml` file.
