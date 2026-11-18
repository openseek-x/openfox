"""Generate tiny, styled OOXML fixtures using independent Office libraries.
Run with the bundled dependency Python; --verify DIR reopens exported candidates.
"""
import sys
from pathlib import Path
from docx import Document
from pptx import Presentation
from pptx.util import Inches
from openpyxl import Workbook, load_workbook
from openpyxl.styles import Font, PatternFill

root = Path(__file__).parent
if len(sys.argv) == 3 and sys.argv[1] == '--verify':
    root = Path(sys.argv[2])
    d = Document(root / 'sample.docx')
    assert d.paragraphs[0].runs[0].text == 'Updated & <text>'
    assert d.paragraphs[0].runs[0].bold
    assert d.paragraphs[0].runs[1].text == ' keep italic' and d.paragraphs[0].runs[1].italic
    p = Presentation(root / 'sample.pptx')
    assert p.slides[0].shapes[0].text == 'Updated & <text>'
    assert p.slides[1].shapes[0].text == 'Keep slide two'
    w = load_workbook(root / 'sample.xlsx')
    assert w['Data']['A1'].value == 'Updated & <text>'
    assert w['Data']['A1'].font.bold
    assert w['Data']['A2'].value == 'Original text'
    assert w['Data']['B1'].value == 12.5
    assert w['Data']['C1'].value == '=B1*2'
    assert w.calculation.fullCalcOnLoad and w.calculation.forceFullCalc
    print('Independent reopen: DOCX / PPTX / XLSX content, styles and formulas OK')
    sys.exit()

d = Document()
p = d.add_paragraph()
p.add_run('Original text').bold = True
p.add_run(' keep italic').italic = True
d.add_paragraph('Keep paragraph two')
d.add_table(rows=1, cols=1).cell(0, 0).text = 'Keep table'
d.sections[0].header.paragraphs[0].text = 'Keep header'
d.save(root / 'sample.docx')
p = Presentation()
for text in ['Original text', 'Keep slide two']:
    slide = p.slides.add_slide(p.slide_layouts[6])
    box = slide.shapes.add_textbox(Inches(1), Inches(1), Inches(7), Inches(1))
    run = box.text_frame.paragraphs[0].add_run()
    run.text = text
    run.font.bold = True
p.save(root / 'sample.pptx')
w = Workbook()
s = w.active
s.title = 'Data'
s['A1'] = 'Original text'
s['A2'] = 'Original text'
s['A1'].font = Font(bold=True, color='FF0000')
s['A1'].fill = PatternFill('solid', fgColor='FFFF00')
s['B1'] = 12.5
s['C1'] = '=B1*2'
s['D1'] = True
w.create_sheet('Second')['A1'] = 'Keep second sheet'
w.save(root / 'sample.xlsx')
print('Generated DOCX / PPTX / XLSX fixtures')
