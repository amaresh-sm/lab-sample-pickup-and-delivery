import { createRouter, createWebHistory } from "vue-router";
import DashboardPage from "./pages/DashboardPage.vue";
import RequestsPage from "./pages/RequestsPage.vue";
import WorkPage from "./pages/WorkPage.vue";
import OperationsPage from "./pages/OperationsPage.vue";
import PlansPage from "./pages/PlansPage.vue";
import CustodyPage from "./pages/CustodyPage.vue";
import AuditPage from "./pages/AuditPage.vue";
import AdminPage from "./pages/AdminPage.vue";

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: "/", redirect: "/dashboard" },
    { path: "/workbench", redirect: "/dashboard" },
    { path: "/tracking", redirect: "/requests" },
    { path: "/dashboard", component: DashboardPage, meta: { title: "Overview" } },
    { path: "/requests", component: RequestsPage, meta: { title: "Requests" } },
    { path: "/work", component: WorkPage, meta: { title: "My work" } },
    { path: "/operations", component: OperationsPage, meta: { title: "Live operations" } },
    { path: "/plans", component: PlansPage, meta: { title: "Journey plans" } },
    { path: "/custody", component: CustodyPage, meta: { title: "Custody" } },
    { path: "/audit", component: AuditPage, meta: { title: "Audit trail" } },
    { path: "/admin", component: AdminPage, meta: { title: "Administration" } },
    { path: "/:pathMatch(.*)*", redirect: "/dashboard" },
  ],
  scrollBehavior: () => ({ top: 0 }),
});
