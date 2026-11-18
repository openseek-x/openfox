"""生成版式回归用的中文、配色、图表 PPT，随后用固定版本 Gotenberg 转换。"""
from pathlib import Path
from pptx import Presentation
from pptx.util import Inches, Pt
from pptx.dml.color import RGBColor
from pptx.chart.data import CategoryChartData
from pptx.enum.chart import XL_CHART_TYPE
p=Presentation()
p.slide_width=Inches(12)
p.slide_height=Inches(6.75)
s=p.slides.add_slide(p.slide_layouts[6])
s.background.fill.solid()
s.background.fill.fore_color.rgb=RGBColor.from_string('13243A')
for text,x,y,size,color in [('中文报告 · 产品进展',.6,.5,30,'FFFFFF'),('Original text',.6,1.35,18,'7DD3FC')]:
 r=s.shapes.add_textbox(Inches(x),Inches(y),Inches(10),Inches(.7)).text_frame.paragraphs[0].add_run()
 r.text=text;r.font.name='Noto Sans CJK SC';r.font.size=Pt(size);r.font.color.rgb=RGBColor.from_string(color)
data=CategoryChartData();data.categories=['第一阶段','第二阶段','第三阶段'];data.add_series('完成率',[30,55,85])
chart=s.shapes.add_chart(XL_CHART_TYPE.COLUMN_CLUSTERED,Inches(.6), Inches(2.3), Inches(10.8), Inches(3.8),data).chart
chart.has_legend=False
chart.chart_style=10
p.save(Path(__file__).with_name('styled.pptx'))
