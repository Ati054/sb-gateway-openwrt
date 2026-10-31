#!/usr/bin/env python3
"""Check package translations, format arguments and shipped LuCI catalogues."""
import ast
from collections import Counter
import json
from pathlib import Path
import re
import struct

ROOT = Path(__file__).resolve().parent.parent
LOCALES = {'en': 'en', 'ru': 'ru', 'zh_Hans': 'zh-cn', 'zh_Hant': 'zh-tw', 'fa': 'fa'}


def read_po(path):
    entries = {}
    message, translated, field = [], [], None

    def flush():
        if not message and not translated:
            return
        key = ''.join(message)
        assert key not in entries, f'{path}: duplicate message {key!r}'
        entries[key] = ''.join(translated)
        message.clear()
        translated.clear()

    for line in path.read_text(encoding='utf-8').splitlines():
        if not line or line.startswith('#'):
            continue
        if line.startswith('msgid '):
            flush()
            message.append(ast.literal_eval(line[6:]))
            field = message
        elif line.startswith('msgstr '):
            translated.append(ast.literal_eval(line[7:]))
            field = translated
        elif line.startswith('"'):
            assert field is not None, f'{path}: orphan continuation'
            field.append(ast.literal_eval(line))
        else:
            raise AssertionError(f'{path}: unsupported PO directive: {line}')
    flush()
    return entries


def compiled_values(path, plural):
    data = path.read_bytes()
    assert len(data) >= 4, f'{path}: missing LMO header'
    index = struct.unpack_from('>I', data, len(data) - 4)[0]
    assert index <= len(data) - 4 and (len(data) - 4 - index) % 16 == 0, f'{path}: invalid LMO index'
    values, keys, header = [], [], None
    for offset in range(index, len(data) - 4, 16):
        key, _plural_count, start, length = struct.unpack_from('>4I', data, offset)
        assert start + length <= index, f'{path}: invalid translation offset'
        text = data[start:start + length].decode('utf-8')
        keys.append(key)
        if key == 0:
            header = text
        else:
            values.append(text)
    assert keys == sorted(set(keys)), f'{path}: unordered or duplicate translation hashes'
    assert header == plural, f'{path}: stale plural rules'
    return Counter(values)


def check():
    english = read_po(ROOT / 'po/en/resproxy.po')
    required = set()
    literal = re.compile(r'''_\(\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")\s*\)''')
    for path in (ROOT / 'htdocs').rglob('*.js'):
        for match in literal.finditer(path.read_text(encoding='utf-8')):
            required.add(ast.literal_eval(match.group(1)))
    menu = json.loads((ROOT / 'files/luci-app-resproxy.json').read_text(encoding='utf-8'))
    required.update(entry['title'] for entry in menu.values())
    packs = json.loads((ROOT / 'internal/rulesets/service-packs.json').read_text(encoding='utf-8'))
    required.update(item[field] for item in packs for field in ('name', 'description') if item.get(field))
    assert required <= english.keys(), f'Messages missing from the catalogues: {sorted(required - english.keys())}'
    for locale, suffix in LOCALES.items():
        messages = read_po(ROOT / f'po/{locale}/resproxy.po')
        assert messages.keys() == english.keys(), f'{locale}: message coverage differs from English'
        for key, value in messages.items():
            assert value, f'{locale}: empty translation for {key!r}'
            if not key:
                continue
            assert key == key.strip(), f'{locale}: message key has surrounding whitespace: {key!r}'
            assert key == key.strip(), f'{locale}: message key has surrounding whitespace: {key!r}'
            assert re.findall(r'%[sdh]', key) == re.findall(r'%[sdh]', value), f'{locale}: format arguments changed in {key!r}'
            assert '\ufffd' not in value, f'{locale}: damaged UTF-8 in {key!r}'
            if locale.startswith('zh') and not re.search('[\u0600-\u06ff]', key):
                assert not re.search('[\u0600-\u06ff]', value), f'{locale}: unexpected Arabic script in {key!r}'
            if locale == 'fa' and not re.search('[\u3400-\u9fff]', key):
                assert not re.search('[\u3400-\u9fff]', value), f'{locale}: unexpected Han script in {key!r}'
        plural = re.search(r'^Plural-Forms: (.+)$', messages[''], re.M).group(1)
        expected = Counter(value for key, value in messages.items() if key and key != value)
        actual = compiled_values(ROOT / f'files/resproxy.{suffix}.lmo', plural)
        assert actual == expected, f'{locale}: compiled LMO differs from PO; rebuild it with po2lmo'
        print(f'{locale}: {len(messages)-1} messages, source coverage and compiled catalogue verified')


if __name__ == '__main__':
    check()
