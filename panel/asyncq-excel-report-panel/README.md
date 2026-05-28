# AsyncQ Excel Report Panel

Panel plugin that calls an AsyncQ datasource resource to generate preconfigured `.xlsx` reports. The report catalog is configured on the datasource through `jsonData.excelReports`.

The panel sends the current dashboard time range, Grafana variables, and its own returned frames to the datasource. While the backend writes the workbook it shows an indeterminate progress bar and elapsed time, then displays the final generation duration and downloads the workbook through a one-time backend download token. Users can edit the generated filename before download; the panel preserves that override even if the report catalog/default filename loads later. Use Grafana's Dashboard datasource in this panel's targets when a report binding should write data from another panel.

For large data-sheet exports, set `writeMode: "stream"` on the report or binding in datasource `jsonData.excelReports`. Stream mode is the fastest Excel writer path and is intended for dedicated template data sheets whose cells are fully owned by AsyncQ. Keep workbook formulas and charts on separate sheets pointing at the populated ranges.
