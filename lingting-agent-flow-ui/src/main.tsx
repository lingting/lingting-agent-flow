import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "@/index.css";
import { AppSidebarLayout, type User, UserStore } from "lingting-react-ui";

import { navigationRoutes, PRODUCT_TITLE, ROOT_REDIRECT_TO } from "@/app/navigation";
import { ProductBrand } from "@/components/product-brand";
import { NotFoundPage } from "@/pages/not-found";

// 当前步骤没有后端认证，始终返回同一个本地用户；不伪造角色与权限。
const fixedUser: User = {
  id: "local",
  nickname: PRODUCT_TITLE,
  organizations: [],
  permission: [],
  roles: [],
  tenantId: "local",
};

UserStore.initialize({
  getUser: async () => ({ type: "login", value: fixedUser }),
  logout: async () => ({ type: "login", value: fixedUser }),
});

const root = document.getElementById("root");

if (!root) {
  throw new Error("未找到应用挂载节点");
}

createRoot(root).render(
  <StrictMode>
    <AppSidebarLayout
      baseItems={[<ProductBrand icon="/favicon.svg" key="product-brand" title={PRODUCT_TITLE} />]}
      logoutPosition="hidden"
      menuRoutes={navigationRoutes}
      notFoundComponent={NotFoundPage}
      rootRedirectTo={ROOT_REDIRECT_TO}
      title={PRODUCT_TITLE}
      userPosition="hidden"
    />
  </StrictMode>,
);
