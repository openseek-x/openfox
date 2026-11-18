"""Independent Office fidelity/anchor fixtures; never modify user documents."""
from pathlib import Path
from docx import Document
from docx.shared import Inches, Pt, RGBColor
from docx.oxml import OxmlElement
from docx.oxml.ns import qn
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill, Alignment
from openpyxl.chart import BarChart, Reference
from openpyxl.drawing.image import Image as SheetImage
from PIL import Image, ImageDraw

root = Path(__file__).parent
logo = Image.new('RGB', (240, 80), '#173551')
draw = ImageDraw.Draw(logo)
draw.rectangle((12, 12, 68, 68), fill='#27C5A2')
draw.ellipse((100, 12, 156, 68), fill='#F5B642')
draw.polygon([(180, 68), (205, 12), (230, 68)], fill='#EF6574')
logo.save(root / 'complex-logo.png')

d = Document()
normal = d.styles['Normal']
normal.font.name = 'Noto Sans SC'
normal.font.size = Pt(11)
normal.element.rPr.rFonts.set(qn('w:eastAsia'), 'Noto Sans SC')
d.sections[0].header.paragraphs[0].text = 'OpenFox · 文档验收 / Header'
footer = d.sections[0].footer.paragraphs[0]
footer.add_run('页码 / Page ')
field = OxmlElement('w:fldSimple')
field.set(qn('w:instr'), 'PAGE')
run = OxmlElement('w:r')
text = OxmlElement('w:t')
text.text = '1'
run.append(text)
field.append(run)
footer._p.append(field)
p = d.add_paragraph()
r = p.add_run('中文报告 · 协作进展')
r.bold = True
r.font.size = Pt(24)
r.font.color.rgb = RGBColor.from_string('173551')
d.add_paragraph('原版式验证：中文、English、表格与图像应保持可读。')
p = d.add_paragraph()
p.add_run('前半段 ').bold = True
p.add_run('重复文本').italic = True
p.add_run(' 后半段；同一句里保留独立的源文字节点。')
d.add_picture(str(root / 'complex-logo.png'), width=Inches(2.4))
table = d.add_table(rows=3, cols=3)
table.style = 'Table Grid'
for row, values in zip(table.rows, [['阶段', '状态', '备注'], ['第一阶段', '完成', '重复文本'], ['第二阶段', '进行中', '合并区域']]):
    for cell, value in zip(row.cells, values):
        cell.text = value
table.cell(2, 1).merge(table.cell(2, 2)).text = '横向合并单元格'
d.add_page_break()
d.add_paragraph('第二页 · 独立分页')
d.add_paragraph('重复文本')
d.add_paragraph('页眉页脚应重复出现；文字选区必须区分相同内容的不同位置。')
d.save(root / 'complex.docx')

w = Workbook()
s = w.active
s.title = '项目进展'
s.merge_cells('A1:D1')
s['A1'] = '中文工作表 · 项目进展'
s['A1'].font = Font(name='Noto Sans SC', size=20, bold=True, color='FFFFFF')
s['A1'].fill = PatternFill('solid', fgColor='173551')
s.row_dimensions[1].height = 38
for row in [['阶段', '计划', '完成', '完成率'], ['第一阶段', 10, 8, '=C3/B3'], ['第二阶段', 20, 12, '=C4/B4'], ['第三阶段', 30, 24, '=C5/B5']]:
    s.append(row)
for row in s.iter_rows(min_row=2, max_row=5, max_col=4):
    for cell in row:
        cell.font = Font(name='Noto Sans SC', size=12, bold=cell.row == 2)
        cell.alignment = Alignment(vertical='center')
        cell.fill = PatternFill('solid', fgColor='E5F4F0' if cell.row % 2 else 'F1F5F9')
for col in 'ABCD':
    s.column_dimensions[col].width = 19
for row in range(3, 6):
    s[f'D{row}'].number_format = '0%'
s['A7'] = '重复文本'
s['B7'] = '重复文本'
s['A8'] = '隐藏行'
s.row_dimensions[8].hidden = True
s.freeze_panes = 'B3'
chart = BarChart()
chart.title = '各阶段完成情况'
chart.add_data(Reference(s, min_col=2, max_col=3, min_row=2, max_row=5), titles_from_data=True)
chart.set_categories(Reference(s, min_col=1, min_row=3, max_row=5))
chart.width = 17
chart.height = 9
s.add_chart(chart, 'F3')
s.add_image(SheetImage(str(root / 'complex-logo.png')), 'F1')
s.print_area = 'A1:O21'
s.sheet_properties.pageSetUpPr.fitToPage = True
s.page_setup.orientation = 'landscape'
s.page_setup.paperSize = s.PAPERSIZE_A4
s.page_setup.fitToWidth = 1
s.page_setup.fitToHeight = 1
w.create_sheet('第二张表')['A1'] = '重复文本'
w.save(root / 'complex.xlsx')
print('Generated two-page DOCX and two-sheet XLSX with duplicate text, images, chart, merge and formulas.')
