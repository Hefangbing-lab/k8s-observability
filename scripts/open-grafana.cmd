@echo off
REM open-grafana.cmd -- open the Grafana 4-panel dashboard
REM Usage: double-click this file, or run  scripts\open-grafana.cmd  from repo root
REM Stop : close the two kubectl windows it opens.

echo [1/3] port-forward Grafana to localhost:3000 ...
start "grafana-forward" cmd /k kubectl -n monitoring port-forward svc/monitoring-grafana 3000:80

echo [2/3] port-forward order-service to localhost:18080 ...
start "order-forward" cmd /k kubectl -n default port-forward svc/order-service 18080:80

echo [3/3] read Grafana admin password ...
for /f "delims=" %%i in ('kubectl -n monitoring get secret monitoring-grafana -o jsonpath^="{.data.admin-password}"') do set "PWD_B64=%%i"
for /f "delims=" %%p in ('powershell -NoProfile -Command "[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%PWD_B64%'))"') do set "PWD_PLAIN=%%p"

echo.
echo ==============================================
echo  Grafana   : http://localhost:3000
echo  Dashboard : http://localhost:3000/d/k8s-obs-services
echo  User      : admin
echo  Password  : %PWD_PLAIN%
echo  Traffic   : http://localhost:18080/order/100?userId=3
echo ==============================================
echo  (close the two kubectl windows to stop forwarding)
echo.

timeout /t 5 /nobreak >nul
start "" "http://localhost:3000/d/k8s-obs-services"
echo Browser opened. If not, visit the Dashboard URL above.
pause
