"""Check renderer anchors against actual ZIP/XML, independently of BetterOffice."""
import hashlib
import json
from pathlib import Path
import xml.etree.ElementTree as ET
from zipfile import ZipFile

ROOT = Path(__file__).resolve().parents[3]
WORK = ROOT / 'work/office-stage6'
FIXTURES = ROOT / 'tests/frontend/e2e/fixtures/office-preview'
EVIDENCE = ROOT / 'docs/development-records/evidence/office-stage6-20261117'
W = '{http://schemas.openxmlformats.org/wordprocessingml/2006/main}'
A = '{http://schemas.openxmlformats.org/drawingml/2006/main}'
P = '{http://schemas.openxmlformats.org/presentationml/2006/main}'
S = '{http://schemas.openxmlformats.org/spreadsheetml/2006/main}'


def load(name):
    return json.loads((WORK / (name + '.json')).read_text())


def walk(value):
    if isinstance(value, dict):
        yield value
        for item in value.values():
            yield from walk(item)
    elif isinstance(value, list):
        for item in value:
            yield from walk(item)


def source_node(package, provenance):
    raw = package.read(provenance['part'].lstrip('/'))
    assert hashlib.sha256(raw).hexdigest() == provenance['partSha256']
    node = ET.fromstring(raw)
    for index in provenance['path']:
        node = list(node)[index]
    return node


report = {}
doc = load('complex.docx')
source = FIXTURES / 'complex.docx'
digest = hashlib.sha256(source.read_bytes()).hexdigest()
assert doc['identities']['packageSha256'] == digest
identities = {(p['session']['story'], p['session']['paraId']): p['source']
              for p in doc['identities']['paragraphs'] if p['session'] and p['source']}
checked, duplicates = 0, []
with ZipFile(source) as package:
    for block in walk(doc['mapped']['content']['structured']):
        if block.get('kind') != 'paragraph' or 'paragraph' not in block:
            continue
        anchor = block['anchor']
        loc = identities[(anchor['story'], anchor['paraId'])]
        assert loc['packageSha256'] == digest
        para = list(ET.fromstring(package.read(loc['partUri'].lstrip('/'))).iter(W + 'p'))[loc['paragraphOrdinal']]
        source_text = ''.join(t.text or '' for t in para.iter(W + 't'))
        inlines = block['paragraph']['inlines']
        if any(item['kind'] != 'text' for item in inlines):
            continue  # Field/image atoms are not plain editable source text.
        rendered_text = ''.join(item['text'] for item in inlines)
        assert source_text == rendered_text, (source_text, rendered_text, loc)
        checked += 1
        if '重复文本' in source_text:
            duplicates.append((loc['partUri'], loc['paragraphOrdinal']))
    fragments = doc['mapped']['content']['layout']['fragments']
    located = [f for f in fragments if f['slice']['kind'] == 'text' and f['geometry']]
    assert len(doc['pages']) == 2 and len(duplicates) == 3 and len(set(duplicates)) == 3
    assert located and all(0 <= f['pageIndex'] < 2 for f in located)
report['docx'] = {'source_paragraphs_checked': checked, 'text_fragments_with_geometry': len(located),
                  'duplicate_text_has_distinct_source_paragraphs': True, 'pages': 2,
                  'fidelity_accepted': False,
                  'manual_visual_review': ['bold/italic differ from baseline', 'table vertical spacing differs from baseline']}

for name in ('styled.pptx', 'user-deck.pptx'):
    result = load(name)
    source = WORK / 'fixtures' / name if name.startswith('user') else FIXTURES / name
    checked = 0
    with ZipFile(source) as package:
        for slide, frame in zip(result['mapped']['content']['slides'], result['frames']):
            shapes = {}
            for shape in walk(slide['shapes']):
                if 'provenance' not in shape or 'stories' not in shape:
                    continue
                provenance = shape['provenance']
                node = source_node(package, provenance)
                ids = list(node.iter(P + 'cNvPr'))
                assert ids and int(ids[0].attrib['id']) == provenance['sourceId']
                expected = ''.join(t.text or '' for t in node.iter(A + 't'))
                actual = ''.join(run.get('text', '') for story in shape['stories'] for para in story['paragraphs'] for run in para['runs'])
                assert expected == actual, (name, expected, actual)
                shapes[shape['anchor']['shapeId']] = provenance['sourceId']
                checked += 1
            for primitive in frame['primitives']:
                if primitive.get('shapeId'):
                    assert primitive['shapeId'] in shapes
    assert len(result['frames']) == (11 if name.startswith('user') else 1)
    report[name] = {'source_shapes_checked': checked, 'slide_count': len(result['frames']),
                    'part_hash_xml_path_shape_id_and_text_match': True}

x = load('complex.xlsx')
checked = 0
with ZipFile(FIXTURES / 'complex.xlsx') as package:
    for sheet in x['mapped']['content']['sheets']:
        source = source_node(package, sheet['source'])
        cells = {c.attrib['r']: c for c in source.iter(S + 'c')}
        for cell in sheet['cells']:
            source_cell = cells[cell['anchor']['a1']]
            if cell['value']['kind'] == 'text':
                assert ''.join(t.text or '' for t in source_cell.iter(S + 't')) == cell['value']['value']
            if cell['formula']:
                assert source_cell.find(S + 'f').text == cell['formula'].lstrip('=')
            checked += 1
assert x['picks'][0]['hit'] == {'row': 2, 'col': 3}
report['xlsx'] = {'source_cells_checked': checked, 'sheet_count': len(x['info']), 'D3_coordinate_roundtrip': True,
                  'fidelity_accepted': False,
                  'defects': ['merged title clipped across frozen column', 'embedded image absent', 'chart without stored cache blank']}

(EVIDENCE / 'source-verification.json').write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
print(json.dumps(report, indent=2, ensure_ascii=False))
