# AsyncQ Excel Report Panel

Panel plugin that calls an AsyncQ datasource resource to generate preconfigured `.xlsx` reports. The report catalog is configured on the datasource through `jsonData.excelReports`.

The panel sends the current dashboard time range, Grafana variables, and its own returned frames to the datasource, then downloads the workbook returned by the backend. Use Grafana's Dashboard datasource in this panel's targets when a report binding should write data from another panel.
